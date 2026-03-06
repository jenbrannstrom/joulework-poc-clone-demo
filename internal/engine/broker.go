package engine

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/joulework/distri-pico/internal/protocol"
)

var hashRegex = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)

type Config struct {
	ChunkDir       string
	ResultDir      string
	LeaseTimeout   time.Duration
	MaxResultBytes int
	BrowserWatts   float64
	LocalWatts     float64
	TargetJoules   float64
}

type Task struct {
	ID   string
	Path string
}

type Lease struct {
	TaskID     string
	LeaseID    string
	SessionID  string
	WorkerType string
	AssignedAt time.Time
	ExpiresAt  time.Time
}

type Assignment struct {
	TaskID         string
	LeaseID        string
	TaskType       string
	PayloadBase64  string
	DeadlineUnixMs int64
}

type ResultRecord struct {
	TaskID         string    `json:"taskId"`
	LeaseID        string    `json:"leaseId"`
	WorkerType     string    `json:"workerType"`
	SessionID      string    `json:"sessionId"`
	ElapsedMs      int64     `json:"elapsedMs"`
	Result         string    `json:"result"`
	OutputHash     string    `json:"outputHash,omitempty"`
	JoulesDeltaEst float64   `json:"joulesDeltaEst"`
	SubmittedAt    time.Time `json:"submittedAt"`
}

type Stats struct {
	ReadyCount   int
	LeasedCount  int
	DoneCount    int
	SessionCount int
}

type Broker struct {
	cfg Config

	mu            sync.Mutex
	tasks         map[string]Task
	readyQueue    []string
	readySet      map[string]struct{}
	leasedByTask  map[string]Lease
	done          map[string]struct{}
	sessionJoules map[string]float64
}

func NewBroker(cfg Config) (*Broker, error) {
	if cfg.ChunkDir == "" || cfg.ResultDir == "" {
		return nil, fmt.Errorf("chunk and result directories are required")
	}
	if cfg.LeaseTimeout <= 0 {
		cfg.LeaseTimeout = 30 * time.Second
	}
	if cfg.MaxResultBytes <= 0 {
		cfg.MaxResultBytes = 1 << 20
	}
	if cfg.BrowserWatts <= 0 {
		cfg.BrowserWatts = 12
	}
	if cfg.LocalWatts <= 0 {
		cfg.LocalWatts = 35
	}
	if cfg.TargetJoules <= 0 {
		cfg.TargetJoules = 20
	}
	if err := os.MkdirAll(cfg.ChunkDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir chunk dir: %w", err)
	}
	if err := os.MkdirAll(cfg.ResultDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir result dir: %w", err)
	}

	b := &Broker{
		cfg:           cfg,
		tasks:         make(map[string]Task),
		readySet:      make(map[string]struct{}),
		leasedByTask:  make(map[string]Lease),
		done:          make(map[string]struct{}),
		sessionJoules: make(map[string]float64),
	}
	if err := b.loadCompleted(); err != nil {
		return nil, err
	}
	return b, nil
}

func (b *Broker) loadCompleted() error {
	entries, err := os.ReadDir(b.cfg.ResultDir)
	if err != nil {
		return fmt.Errorf("read result dir: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".result.json") {
			continue
		}
		taskID := strings.TrimSuffix(name, ".result.json")
		if taskID != "" {
			b.done[taskID] = struct{}{}
		}
	}
	return nil
}

func (b *Broker) ScanChunks() error {
	entries, err := os.ReadDir(b.cfg.ChunkDir)
	if err != nil {
		return fmt.Errorf("read chunk dir: %w", err)
	}

	tasks := make([]Task, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		tasks = append(tasks, Task{ID: name, Path: filepath.Join(b.cfg.ChunkDir, name)})
	}
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].ID < tasks[j].ID
	})

	b.mu.Lock()
	defer b.mu.Unlock()

	for _, task := range tasks {
		if _, ok := b.tasks[task.ID]; !ok {
			b.tasks[task.ID] = task
		}
		if _, done := b.done[task.ID]; done {
			continue
		}
		if _, leased := b.leasedByTask[task.ID]; leased {
			continue
		}
		if _, queued := b.readySet[task.ID]; queued {
			continue
		}
		b.readyQueue = append(b.readyQueue, task.ID)
		b.readySet[task.ID] = struct{}{}
	}
	return nil
}

func (b *Broker) TargetJoules() float64 {
	return b.cfg.TargetJoules
}

func (b *Broker) AssignTask(sessionID, workerType string, now time.Time) (Assignment, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Keep scheduling fair and prevent one flaky worker from hoarding leases.
	for _, lease := range b.leasedByTask {
		if lease.SessionID == sessionID {
			return Assignment{}, false, nil
		}
	}

	for len(b.readyQueue) > 0 {
		taskID := b.readyQueue[0]
		b.readyQueue = b.readyQueue[1:]
		delete(b.readySet, taskID)

		if _, done := b.done[taskID]; done {
			continue
		}
		if _, leased := b.leasedByTask[taskID]; leased {
			continue
		}

		task, ok := b.tasks[taskID]
		if !ok {
			continue
		}
		payload, err := os.ReadFile(task.Path)
		if err != nil {
			return Assignment{}, false, fmt.Errorf("read chunk %s: %w", taskID, err)
		}
		leaseID, err := randomID(12)
		if err != nil {
			return Assignment{}, false, fmt.Errorf("generate lease id: %w", err)
		}
		lease := Lease{
			TaskID:     taskID,
			LeaseID:    leaseID,
			SessionID:  sessionID,
			WorkerType: workerType,
			AssignedAt: now,
			ExpiresAt:  now.Add(b.cfg.LeaseTimeout),
		}
		b.leasedByTask[taskID] = lease
		return Assignment{
			TaskID:         taskID,
			LeaseID:        leaseID,
			TaskType:       "sha256",
			PayloadBase64:  base64.StdEncoding.EncodeToString(payload),
			DeadlineUnixMs: lease.ExpiresAt.UnixMilli(),
		}, true, nil
	}
	return Assignment{}, false, nil
}

func (b *Broker) RequeueExpired(now time.Time) int {
	b.mu.Lock()
	defer b.mu.Unlock()

	requeued := 0
	for taskID, lease := range b.leasedByTask {
		if now.Before(lease.ExpiresAt) {
			continue
		}
		delete(b.leasedByTask, taskID)
		if _, done := b.done[taskID]; done {
			continue
		}
		if _, queued := b.readySet[taskID]; queued {
			continue
		}
		b.readyQueue = append(b.readyQueue, taskID)
		b.readySet[taskID] = struct{}{}
		requeued++
	}
	return requeued
}

func (b *Broker) SubmitResult(sessionID, workerType string, req protocol.SubmitResult, now time.Time) protocol.Ack {
	ack := protocol.Ack{
		Type:         protocol.TypeAck,
		TaskID:       req.TaskID,
		TargetJoules: b.cfg.TargetJoules,
	}

	if req.TaskID == "" || req.LeaseID == "" {
		ack.Accepted = false
		ack.Reason = "taskId and leaseId required"
		return ack
	}
	if req.ElapsedMs <= 0 || req.ElapsedMs > 300000 {
		ack.Accepted = false
		ack.Reason = "elapsedMs out of bounds"
		return ack
	}
	if len(req.Result) == 0 {
		ack.Accepted = false
		ack.Reason = "result required"
		return ack
	}
	if len(req.Result) > b.cfg.MaxResultBytes {
		ack.Accepted = false
		ack.Reason = "result too large"
		return ack
	}
	if req.OutputHash != "" && !hashRegex.MatchString(req.OutputHash) {
		ack.Accepted = false
		ack.Reason = "invalid outputHash"
		return ack
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if _, done := b.done[req.TaskID]; done {
		ack.Accepted = true
		ack.Reason = "duplicate_ignored"
		ack.SessionJoulesEst = b.sessionJoules[sessionID]
		ack.TargetReached = ack.SessionJoulesEst >= b.cfg.TargetJoules
		return ack
	}

	lease, ok := b.leasedByTask[req.TaskID]
	if !ok {
		ack.Accepted = false
		ack.Reason = "no_active_lease"
		return ack
	}
	if lease.LeaseID != req.LeaseID {
		ack.Accepted = false
		ack.Reason = "lease_mismatch"
		return ack
	}
	if lease.SessionID != sessionID {
		ack.Accepted = false
		ack.Reason = "wrong_session"
		return ack
	}
	if now.After(lease.ExpiresAt) {
		delete(b.leasedByTask, req.TaskID)
		if _, queued := b.readySet[req.TaskID]; !queued {
			b.readyQueue = append(b.readyQueue, req.TaskID)
			b.readySet[req.TaskID] = struct{}{}
		}
		ack.Accepted = false
		ack.Reason = "lease_expired_requeued"
		return ack
	}

	joulesDelta := elapsedMsToJoules(req.ElapsedMs, b.wattsForWorker(workerType))
	record := ResultRecord{
		TaskID:         req.TaskID,
		LeaseID:        req.LeaseID,
		WorkerType:     workerType,
		SessionID:      sessionID,
		ElapsedMs:      req.ElapsedMs,
		Result:         req.Result,
		OutputHash:     req.OutputHash,
		JoulesDeltaEst: joulesDelta,
		SubmittedAt:    now.UTC(),
	}
	if err := writeJSONAtomic(filepath.Join(b.cfg.ResultDir, req.TaskID+".result.json"), record); err != nil {
		ack.Accepted = false
		ack.Reason = "persist_failed"
		return ack
	}

	delete(b.leasedByTask, req.TaskID)
	b.done[req.TaskID] = struct{}{}

	sessionTotal := b.sessionJoules[sessionID] + joulesDelta
	b.sessionJoules[sessionID] = sessionTotal

	ack.Accepted = true
	ack.JoulesDeltaEst = joulesDelta
	ack.SessionJoulesEst = sessionTotal
	ack.TargetReached = sessionTotal >= b.cfg.TargetJoules
	return ack
}

func (b *Broker) Stats() Stats {
	b.mu.Lock()
	defer b.mu.Unlock()
	return Stats{
		ReadyCount:   len(b.readyQueue),
		LeasedCount:  len(b.leasedByTask),
		DoneCount:    len(b.done),
		SessionCount: len(b.sessionJoules),
	}
}

func (b *Broker) RegisterSession(sessionID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.sessionJoules[sessionID]; !ok {
		b.sessionJoules[sessionID] = 0
	}
}

func (b *Broker) SessionJoules(sessionID string) float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sessionJoules[sessionID]
}

func (b *Broker) wattsForWorker(workerType string) float64 {
	switch workerType {
	case protocol.WorkerTypeBrowser:
		return b.cfg.BrowserWatts
	default:
		return b.cfg.LocalWatts
	}
}

func elapsedMsToJoules(elapsedMs int64, watts float64) float64 {
	seconds := float64(elapsedMs) / 1000.0
	return seconds * watts
}

func writeJSONAtomic(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal result: %w", err)
	}
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("write temp result: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename result: %w", err)
	}
	return nil
}

func randomID(nBytes int) (string, error) {
	buf := make([]byte, nBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
