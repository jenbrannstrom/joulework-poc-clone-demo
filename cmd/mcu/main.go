package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/joulework/distri-pico/internal/engine"
	"github.com/joulework/distri-pico/internal/protocol"
)

type server struct {
	broker         *engine.Broker
	allowedOrigins map[string]struct{}
	upgrader       websocket.Upgrader
}

func main() {
	var (
		addr          = flag.String("addr", ":8080", "HTTP listen address")
		chunkDir      = flag.String("chunk-dir", "./data/chunks", "Directory with chunk files")
		resultDir     = flag.String("result-dir", "./data/results", "Directory to persist result files")
		scanInterval  = flag.Duration("scan-interval", 2*time.Second, "How often to scan chunk directory")
		reapInterval  = flag.Duration("reap-interval", 1*time.Second, "How often to requeue expired leases")
		leaseTimeout  = flag.Duration("lease-timeout", 30*time.Second, "Lease timeout per task")
		browserWatts  = flag.Float64("browser-watts", 12.0, "Estimated watts for browser workers")
		localWatts    = flag.Float64("local-watts", 35.0, "Estimated watts for local workers")
		targetJoules  = flag.Float64("target-joules", 20.0, "Target estimated joules per browser session")
		maxResultSize = flag.Int("max-result-bytes", 1<<20, "Maximum accepted result payload bytes")
		allowOrigins  = flag.String("allow-origins", "", "Comma-separated origin allowlist, empty allows all")
	)
	flag.Parse()

	broker, err := engine.NewBroker(engine.Config{
		ChunkDir:       *chunkDir,
		ResultDir:      *resultDir,
		LeaseTimeout:   *leaseTimeout,
		MaxResultBytes: *maxResultSize,
		BrowserWatts:   *browserWatts,
		LocalWatts:     *localWatts,
		TargetJoules:   *targetJoules,
	})
	if err != nil {
		log.Fatalf("init broker: %v", err)
	}
	if err := broker.ScanChunks(); err != nil {
		log.Printf("initial scan warning: %v", err)
	}

	s := &server{
		broker:         broker,
		allowedOrigins: parseOrigins(*allowOrigins),
	}
	s.upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin:     s.checkOrigin,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/node", s.handleNode)

	httpSrv := &http.Server{
		Addr:    *addr,
		Handler: mux,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		ticker := time.NewTicker(*scanInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := broker.ScanChunks(); err != nil {
					log.Printf("scan chunks: %v", err)
				}
			}
		}
	}()

	go func() {
		ticker := time.NewTicker(*reapInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				requeued := broker.RequeueExpired(now)
				if requeued > 0 {
					log.Printf("requeued %d expired leases", requeued)
				}
			}
		}
	}()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()

	log.Printf("mcu listening on %s", *addr)
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("http server failed: %v", err)
	}
}

func (s *server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	stats := s.broker.Stats()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":       true,
		"ready":    stats.ReadyCount,
		"leased":   stats.LeasedCount,
		"done":     stats.DoneCount,
		"sessions": stats.SessionCount,
	})
}

func (s *server) handleNode(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("websocket upgrade: %v", err)
		return
	}
	defer conn.Close()

	workerType := normalizeWorkerType(r.URL.Query().Get("workerType"))
	sessionID := r.URL.Query().Get("sessionId")
	if sessionID == "" {
		sessionID = mustRandomID(8)
	}
	helloSeen := false

	helloAck := protocol.HelloAck{
		Type:         protocol.TypeHelloAck,
		SessionID:    sessionID,
		TargetJoules: s.broker.TargetJoules(),
	}

	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				return
			}
			log.Printf("read message: %v", err)
			return
		}

		messageType, err := protocol.DecodeType(payload)
		if err != nil {
			s.writeError(conn, "invalid envelope")
			continue
		}

		switch messageType {
		case protocol.TypeHello:
			var hello protocol.Hello
			if err := json.Unmarshal(payload, &hello); err != nil {
				s.writeError(conn, "invalid hello")
				continue
			}
			if hello.WorkerType != "" {
				workerType = normalizeWorkerType(hello.WorkerType)
			}
			if hello.SessionID != "" {
				sessionID = hello.SessionID
			}
			s.broker.RegisterSession(sessionID)
			helloAck.SessionID = sessionID
			helloAck.TargetJoules = s.broker.TargetJoules()
			if err := conn.WriteJSON(helloAck); err != nil {
				log.Printf("write hello ack: %v", err)
				return
			}
			helloSeen = true
		case protocol.TypeRequestTask:
			if !helloSeen {
				s.writeError(conn, "hello required before requesting tasks")
				continue
			}
			assignment, ok, err := s.broker.AssignTask(sessionID, workerType, time.Now())
			if err != nil {
				s.writeError(conn, "task assignment failed")
				continue
			}
			if !ok {
				if err := conn.WriteJSON(protocol.NoTask{Type: protocol.TypeNoTask, RetryMs: 1000}); err != nil {
					log.Printf("write no_task: %v", err)
					return
				}
				continue
			}
			if err := conn.WriteJSON(protocol.TaskAssigned{
				Type:           protocol.TypeTaskAssigned,
				TaskID:         assignment.TaskID,
				LeaseID:        assignment.LeaseID,
				TaskType:       assignment.TaskType,
				PayloadBase64:  assignment.PayloadBase64,
				DeadlineUnixMs: assignment.DeadlineUnixMs,
			}); err != nil {
				log.Printf("write task_assigned: %v", err)
				return
			}
		case protocol.TypeSubmitResult:
			if !helloSeen {
				s.writeError(conn, "hello required before submitting results")
				continue
			}
			var req protocol.SubmitResult
			if err := json.Unmarshal(payload, &req); err != nil {
				s.writeError(conn, "invalid submit_result")
				continue
			}
			ack := s.broker.SubmitResult(sessionID, workerType, req, time.Now())
			if err := conn.WriteJSON(ack); err != nil {
				log.Printf("write ack: %v", err)
				return
			}
		default:
			s.writeError(conn, fmt.Sprintf("unsupported message type: %s", messageType))
		}
	}
}

func (s *server) writeError(conn *websocket.Conn, reason string) {
	if err := conn.WriteJSON(protocol.ErrorMessage{Type: protocol.TypeError, Reason: reason}); err != nil {
		log.Printf("write error message: %v", err)
	}
}

func (s *server) checkOrigin(r *http.Request) bool {
	if len(s.allowedOrigins) == 0 {
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	_, ok := s.allowedOrigins[strings.ToLower(u.Scheme+"://"+u.Host)]
	return ok
}

func parseOrigins(value string) map[string]struct{} {
	origins := make(map[string]struct{})
	for _, raw := range strings.Split(value, ",") {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		origins[strings.ToLower(trimmed)] = struct{}{}
	}
	return origins
}

func normalizeWorkerType(workerType string) string {
	switch strings.ToLower(workerType) {
	case protocol.WorkerTypeBrowser:
		return protocol.WorkerTypeBrowser
	default:
		return protocol.WorkerTypeLocal
	}
}

func mustRandomID(nBytes int) string {
	buf := make([]byte, nBytes)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return hex.EncodeToString(buf)
}
