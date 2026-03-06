package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/joulework/distri-pico/internal/engine"
	"github.com/joulework/distri-pico/internal/protocol"
)

type server struct {
	broker         *engine.Broker
	allowedOrigins map[string]struct{}
	unlockSigner   *unlockTokenSigner
	mu             sync.Mutex
	activeWorkers  map[string]activeWorker
	upgrader       websocket.Upgrader
}

type activeWorker struct {
	SessionID   string    `json:"sessionId"`
	WorkerType  string    `json:"workerType"`
	ConnectedAt time.Time `json:"connectedAt"`
	LastSeenAt  time.Time `json:"lastSeenAt"`
}

func main() {
	defaultUnlockSecret := strings.TrimSpace(os.Getenv("JW_UNLOCK_TOKEN_SECRET"))
	if defaultUnlockSecret == "" {
		defaultUnlockSecret = "joulework-poc-insecure-dev-secret"
	}
	var (
		addr              = flag.String("addr", ":8080", "HTTP listen address")
		chunkDir          = flag.String("chunk-dir", "./data/chunks", "Directory with chunk files")
		resultDir         = flag.String("result-dir", "./data/results", "Directory to persist result files")
		scanInterval      = flag.Duration("scan-interval", 2*time.Second, "How often to scan chunk directory")
		reapInterval      = flag.Duration("reap-interval", 1*time.Second, "How often to requeue expired leases")
		leaseTimeout      = flag.Duration("lease-timeout", 30*time.Second, "Lease timeout per task")
		browserWatts      = flag.Float64("browser-watts", 12.0, "Estimated watts for browser workers")
		localWatts        = flag.Float64("local-watts", 35.0, "Estimated watts for local workers")
		targetJoules      = flag.Float64("target-joules", 20.0, "Target estimated joules per browser session")
		maxResultSize     = flag.Int("max-result-bytes", 1<<20, "Maximum accepted result payload bytes")
		allowOrigins      = flag.String("allow-origins", "", "Comma-separated origin allowlist, empty allows all")
		unlockTokenSecret = flag.String("unlock-token-secret", defaultUnlockSecret, "HMAC secret used to sign demo unlock tokens")
		unlockTokenTTL    = flag.Duration("unlock-token-ttl", 15*time.Minute, "TTL for signed demo unlock tokens")
		unlockTokenIssuer = flag.String("unlock-token-issuer", "joulework-mcu", "Issuer value for signed demo unlock tokens")
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
		unlockSigner:   newUnlockTokenSigner([]byte(*unlockTokenSecret), *unlockTokenTTL, *unlockTokenIssuer),
		activeWorkers:  make(map[string]activeWorker),
	}
	s.upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin:     s.checkOrigin,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/demo/progress", s.handleDemoProgress)
	mux.HandleFunc("/demo/unlock_token", s.handleDemoUnlockToken)
	mux.HandleFunc("/node", s.handleNode)
	mux.HandleFunc("/", s.handleRoot)

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

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if s.writeCORSHeaders(w, r) {
		return
	}
	stats := s.broker.Stats()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":       true,
		"total":    stats.TotalCount,
		"ready":    stats.ReadyCount,
		"leased":   stats.LeasedCount,
		"done":     stats.DoneCount,
		"sessions": stats.SessionCount,
	})
}

func (s *server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Query().Get("format") == "json" {
		if s.writeCORSHeaders(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"service": "joulework-mcu",
			"message": "API and worker endpoint. Use the demo page for UI.",
			"endpoints": map[string]string{
				"health":        "/health",
				"demoProgress":  "/demo/progress",
				"demoUnlock":    "/demo/unlock_token?sessionId=...",
				"websocketNode": "/node?workerType=browser",
				"demoPage":      "http://joulework-demo.rtb.cat/",
			},
		})
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, rootPageHTML)
}

func (s *server) handleDemoProgress(w http.ResponseWriter, r *http.Request) {
	if s.writeCORSHeaders(w, r) {
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	stats := s.broker.Stats()
	activeWorkers := s.snapshotActiveWorkers()
	requestedSession := strings.TrimSpace(r.URL.Query().Get("sessionId"))
	mySessionJoules := 0.0
	if requestedSession != "" {
		mySessionJoules = s.broker.SessionJoules(requestedSession)
	}
	activeBrowser := 0
	activeLocal := 0
	for _, worker := range activeWorkers {
		switch worker.WorkerType {
		case protocol.WorkerTypeBrowser:
			activeBrowser++
		default:
			activeLocal++
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":           true,
		"targetJoules": s.broker.TargetJoules(),
		"mySession": map[string]any{
			"id":        requestedSession,
			"joulesEst": mySessionJoules,
		},
		"queue": map[string]any{
			"total":  stats.TotalCount,
			"ready":  stats.ReadyCount,
			"leased": stats.LeasedCount,
			"done":   stats.DoneCount,
		},
		"workers": map[string]any{
			"active":          len(activeWorkers),
			"activeBrowser":   activeBrowser,
			"activeLocal":     activeLocal,
			"knownSessions":   stats.SessionCount,
			"activeSnapshots": activeWorkers,
		},
		"activeLeases":      s.broker.ActiveLeases(12),
		"recentCompletions": s.broker.RecentCompletions(16),
		"pi":                s.broker.PiSnapshot(),
		"now":               time.Now().UTC().Format(time.RFC3339),
	})
}

func (s *server) handleDemoUnlockToken(w http.ResponseWriter, r *http.Request) {
	if s.writeCORSHeaders(w, r) {
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	sessionID := strings.TrimSpace(r.URL.Query().Get("sessionId"))
	if sessionID == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":       false,
			"eligible": false,
			"reason":   "sessionId_required",
		})
		return
	}

	siteHost := normalizeSiteHost(r.URL.Query().Get("siteHost"))
	sessionJoules := s.broker.SessionJoules(sessionID)
	targetJoules := s.broker.TargetJoules()

	if sessionJoules < targetJoules {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":            false,
			"eligible":      false,
			"reason":        "target_not_reached",
			"sessionId":     sessionID,
			"siteHost":      siteHost,
			"sessionJoules": sessionJoules,
			"targetJoules":  targetJoules,
		})
		return
	}

	token, claims, err := s.unlockSigner.Issue(sessionID, siteHost, sessionJoules, targetJoules)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":       false,
			"eligible": false,
			"reason":   "token_issue_failed",
		})
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":            true,
		"eligible":      true,
		"sessionId":     sessionID,
		"siteHost":      siteHost,
		"sessionJoules": sessionJoules,
		"targetJoules":  targetJoules,
		"tokenType":     claims.Version,
		"token":         token,
		"issuedAt":      time.Unix(claims.IssuedAt, 0).UTC().Format(time.RFC3339),
		"expiresAt":     time.Unix(claims.ExpiresAt, 0).UTC().Format(time.RFC3339),
	})
}

func (s *server) handleNode(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("websocket upgrade: %v", err)
		return
	}
	connID := mustRandomID(6)
	tracked := false
	defer func() {
		if tracked {
			s.unregisterActiveWorker(connID)
		}
		conn.Close()
	}()

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
			s.trackActiveWorker(connID, sessionID, workerType)
			tracked = true
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
			s.trackActiveWorker(connID, sessionID, workerType)
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
			s.trackActiveWorker(connID, sessionID, workerType)
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
	return s.originAllowed(origin)
}

func (s *server) writeCORSHeaders(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin != "" {
		if len(s.allowedOrigins) == 0 || s.originAllowed(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
	} else if len(s.allowedOrigins) == 0 {
		w.Header().Set("Access-Control-Allow-Origin", "*")
	}

	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.WriteHeader(http.StatusNoContent)
		return true
	}
	return false
}

func (s *server) originAllowed(origin string) bool {
	if len(s.allowedOrigins) == 0 {
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

type unlockTokenSigner struct {
	secret []byte
	ttl    time.Duration
	issuer string
}

type unlockTokenClaims struct {
	Version      string  `json:"v"`
	Issuer       string  `json:"iss"`
	SessionID    string  `json:"sid"`
	SiteHost     string  `json:"site,omitempty"`
	Joules       float64 `json:"joules"`
	TargetJoules float64 `json:"targetJoules"`
	IssuedAt     int64   `json:"iat"`
	ExpiresAt    int64   `json:"exp"`
}

func newUnlockTokenSigner(secret []byte, ttl time.Duration, issuer string) *unlockTokenSigner {
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	issuer = strings.TrimSpace(issuer)
	if issuer == "" {
		issuer = "joulework-mcu"
	}

	trimmedSecret := strings.TrimSpace(string(secret))
	if trimmedSecret == "" {
		trimmedSecret = "joulework-poc-insecure-dev-secret"
	}
	return &unlockTokenSigner{
		secret: []byte(trimmedSecret),
		ttl:    ttl,
		issuer: issuer,
	}
}

func (s *unlockTokenSigner) Issue(sessionID, siteHost string, joules, targetJoules float64) (string, unlockTokenClaims, error) {
	now := time.Now().UTC()
	claims := unlockTokenClaims{
		Version:      "JWP1",
		Issuer:       s.issuer,
		SessionID:    sessionID,
		SiteHost:     siteHost,
		Joules:       joules,
		TargetJoules: targetJoules,
		IssuedAt:     now.Unix(),
		ExpiresAt:    now.Add(s.ttl).Unix(),
	}

	payloadJSON, err := json.Marshal(claims)
	if err != nil {
		return "", unlockTokenClaims{}, fmt.Errorf("marshal claims: %w", err)
	}
	payloadPart := base64.RawURLEncoding.EncodeToString(payloadJSON)

	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte(payloadPart))
	signaturePart := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	token := "jwp1." + payloadPart + "." + signaturePart
	return token, claims, nil
}

func normalizeSiteHost(raw string) string {
	host := strings.ToLower(strings.TrimSpace(raw))
	if host == "" {
		return ""
	}
	if strings.Contains(host, "://") {
		if parsed, err := url.Parse(host); err == nil {
			host = parsed.Host
		}
	}
	if idx := strings.IndexAny(host, "/?#"); idx >= 0 {
		host = host[:idx]
	}
	if normalized, _, err := net.SplitHostPort(host); err == nil {
		host = normalized
	}
	if len(host) > 120 {
		host = host[:120]
	}
	if host == "" {
		return ""
	}
	for _, ch := range host {
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '.' || ch == '-' {
			continue
		}
		return ""
	}
	return host
}

func (s *server) trackActiveWorker(connID, sessionID, workerType string) {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()

	existing, ok := s.activeWorkers[connID]
	if !ok {
		existing.ConnectedAt = now
	}
	existing.LastSeenAt = now
	existing.SessionID = sessionID
	existing.WorkerType = workerType
	s.activeWorkers[connID] = existing
}

func (s *server) unregisterActiveWorker(connID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.activeWorkers, connID)
}

func (s *server) snapshotActiveWorkers() []activeWorker {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.activeWorkers) == 0 {
		return nil
	}
	snapshots := make([]activeWorker, 0, len(s.activeWorkers))
	for _, worker := range s.activeWorkers {
		snapshots = append(snapshots, worker)
	}
	sort.Slice(snapshots, func(i, j int) bool {
		return snapshots[i].LastSeenAt.After(snapshots[j].LastSeenAt)
	})
	return snapshots
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

const rootPageHTML = `<!doctype html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <title>JouleWork Monitor</title>
    <style>
      :root {
        --bg: #0b1117;
        --card: #131f29;
        --line: #2a4152;
        --ink: #ecf9ff;
        --muted: #9bbecf;
        --accent: #5ac8f7;
      }
      * {
        box-sizing: border-box;
      }
      body {
        margin: 0;
        background: radial-gradient(circle at 20% 0%, #14273a 0%, #081018 55%, #05090f 100%);
        color: var(--ink);
        font-family: "Space Grotesk", "Segoe UI", sans-serif;
      }
      .wrap {
        width: min(760px, 100% - 24px);
        margin: 22px auto 30px;
      }
      .hero {
        border: 1px solid var(--line);
        background: rgba(11, 20, 30, 0.82);
        border-radius: 16px;
        padding: 16px;
      }
      .hero h1 {
        margin: 0;
        font-size: clamp(20px, 4vw, 30px);
      }
      .hero p {
        margin: 8px 0 0;
        color: var(--muted);
      }
      .hero a {
        color: var(--accent);
      }
      .grid {
        margin-top: 12px;
        display: grid;
        grid-template-columns: repeat(4, minmax(0, 1fr));
        gap: 8px;
      }
      .metric {
        border: 1px solid #2b4354;
        border-radius: 10px;
        background: var(--card);
        padding: 9px 10px;
      }
      .metric-head {
        display: flex;
        align-items: center;
        gap: 6px;
      }
      .metric .k {
        font-size: 11px;
        color: var(--muted);
        text-transform: uppercase;
        letter-spacing: 0.06em;
      }
      .info-dot {
        position: relative;
        border: 1px solid #3a596e;
        background: #122331;
        color: #bfe5f8;
        width: 16px;
        height: 16px;
        border-radius: 999px;
        font-size: 10px;
        line-height: 1;
        padding: 0;
        display: inline-flex;
        align-items: center;
        justify-content: center;
        cursor: help;
      }
      .info-dot::after {
        content: attr(data-tip);
        position: absolute;
        left: 0;
        bottom: calc(100% + 8px);
        min-width: 170px;
        max-width: 220px;
        background: #07111a;
        color: #d9eef8;
        border: 1px solid #2f485a;
        border-radius: 8px;
        padding: 7px 8px;
        text-transform: none;
        letter-spacing: normal;
        font-size: 11px;
        line-height: 1.35;
        text-align: left;
        white-space: normal;
        opacity: 0;
        transform: translateY(3px);
        pointer-events: none;
        transition: opacity 0.16s ease, transform 0.16s ease;
        z-index: 20;
      }
      .info-dot:hover::after,
      .info-dot:focus-visible::after {
        opacity: 1;
        transform: translateY(0);
      }
      .metric .v {
        display: block;
        margin-top: 4px;
        font-size: 20px;
        font-weight: 700;
      }
      .session {
        margin-top: 10px;
        border: 1px solid #2b4354;
        border-radius: 10px;
        background: #0d1822;
        padding: 10px;
        color: #d0f0ff;
        font-size: 14px;
      }
      .lists {
        margin-top: 12px;
        display: grid;
        grid-template-columns: repeat(2, minmax(0, 1fr));
        gap: 10px;
      }
      .panel {
        border: 1px solid #2a4152;
        border-radius: 10px;
        background: #0d1720;
        padding: 10px;
      }
      .panel h2 {
        margin: 0;
        font-size: 13px;
        color: #b5d8e8;
        text-transform: uppercase;
        letter-spacing: 0.08em;
      }
      .list {
        margin: 8px 0 0;
        padding: 0;
        list-style: none;
        font-size: 13px;
      }
      .list li {
        border-top: 1px solid rgba(160, 198, 214, 0.15);
        padding: 6px 0;
      }
      .list li:first-child {
        border-top: 0;
      }
      .updated {
        margin-top: 8px;
        color: var(--muted);
        font-size: 12px;
      }
      @media (max-width: 780px) {
        .grid {
          grid-template-columns: repeat(2, minmax(0, 1fr));
        }
        .lists {
          grid-template-columns: 1fr;
        }
      }
    </style>
  </head>
  <body>
    <main class="wrap">
      <section class="hero">
        <h1>JouleWork Monitor</h1>
        <p>
          This page shows swarm-level work from the MCU (Master Coordination Unit).
        </p>
        <p>
          The MCU assigns task leases, accepts results, and tracks compute contribution. Reader-facing demo is
          <a href="http://joulework-demo.rtb.cat/">joulework-demo.rtb.cat</a>.
        </p>
        <div class="grid">
          <div class="metric">
            <div class="metric-head">
              <span class="k">Done</span>
              <button class="info-dot" type="button" aria-label="Done means completed tasks with accepted results." data-tip="Completed tasks with accepted results persisted by the MCU.">i</button>
            </div>
            <span class="v" id="done">0</span>
          </div>
          <div class="metric">
            <div class="metric-head">
              <span class="k">Leased</span>
              <button class="info-dot" type="button" aria-label="Leased means tasks currently assigned to workers." data-tip="Tasks currently assigned to workers and still in-flight (not yet submitted). If tasks are short, it often reads 0 between assignments.">i</button>
            </div>
            <span class="v" id="leased">0</span>
          </div>
          <div class="metric">
            <div class="metric-head">
              <span class="k">Ready</span>
              <button class="info-dot" type="button" aria-label="Ready means queued tasks waiting for workers." data-tip="Tasks queued and waiting to be leased to an active worker.">i</button>
            </div>
            <span class="v" id="ready">0</span>
          </div>
          <div class="metric">
            <div class="metric-head">
              <span class="k">Workers</span>
              <button class="info-dot" type="button" aria-label="Workers means currently connected clients." data-tip="Active browser/local worker connections currently seen by the MCU.">i</button>
            </div>
            <span class="v" id="workers">0</span>
          </div>
        </div>
        <div class="session" id="session-box">Session contribution: add <code>?sessionId=...</code> to this URL.</div>
      </section>
      <section class="lists">
        <article class="panel">
          <h2>Recent Completions</h2>
          <ul class="list" id="recent"><li>No completions yet.</li></ul>
        </article>
        <article class="panel">
          <h2>Active Leases</h2>
          <ul class="list" id="leases"><li>No active leases.</li></ul>
        </article>
      </section>
      <p class="updated" id="updated">loading...</p>
    </main>
    <script>
      const params = new URLSearchParams(window.location.search);
      const sessionId = params.get("sessionId") || "";
      const doneEl = document.getElementById("done");
      const leasedEl = document.getElementById("leased");
      const readyEl = document.getElementById("ready");
      const workersEl = document.getElementById("workers");
      const recentEl = document.getElementById("recent");
      const leasesEl = document.getElementById("leases");
      const sessionBox = document.getElementById("session-box");
      const updatedEl = document.getElementById("updated");

      const ago = (iso) => {
        const ts = Date.parse(iso || "");
        if (Number.isNaN(ts)) {
          return "";
        }
        const s = Math.max(0, Math.round((Date.now() - ts) / 1000));
        if (s < 60) return s + "s ago";
        const m = Math.floor(s / 60);
        if (m < 60) return m + "m ago";
        return Math.floor(m / 60) + "h ago";
      };

      const renderList = (root, rows, fallback) => {
        root.innerHTML = "";
        if (!Array.isArray(rows) || rows.length === 0) {
          const li = document.createElement("li");
          li.textContent = fallback;
          root.appendChild(li);
          return;
        }
        rows.forEach((row) => {
          const li = document.createElement("li");
          li.textContent = row;
          root.appendChild(li);
        });
      };

      const refresh = async () => {
        const query = sessionId ? "?sessionId=" + encodeURIComponent(sessionId) : "";
        const response = await fetch("/demo/progress" + query, { cache: "no-store" });
        const payload = await response.json();
        const queue = payload.queue || {};
        const workers = payload.workers || {};
        const pi = payload.pi || {};

        doneEl.textContent = String(queue.done || 0);
        leasedEl.textContent = String(queue.leased || 0);
        readyEl.textContent = String(queue.ready || 0);
        workersEl.textContent = String(workers.active || 0);

        const recent = (payload.recentCompletions || []).slice(0, 8).map((item) => {
          return (item.taskId || "task") + " via " + (item.workerType || "worker") + " (" + (item.elapsedMs || 0) + "ms, " + ago(item.submittedAt) + ")";
        });
        renderList(recentEl, recent, "No completions yet.");

        const leases = (payload.activeLeases || []).slice(0, 8).map((item) => {
          return (item.taskId || "task") + " assigned to " + (item.workerType || "worker");
        });
        renderList(leasesEl, leases, "No active leases.");

        if (sessionId) {
          const my = Number((payload.mySession || {}).joulesEst || 0);
          sessionBox.textContent = "Session " + sessionId + ": " + my.toFixed(2) + "J estimated contribution.";
        } else {
          sessionBox.innerHTML = "Session contribution: add <code>?sessionId=...</code> to this URL.";
        }

        updatedEl.textContent =
          "Swarm progress: " +
          (queue.done || 0) +
          "/" +
          (queue.total || 0) +
          " tasks. PI estimate: " +
          Number(pi.estimate || 0).toFixed(10) +
          ". Updated " +
          (ago(payload.now) || "just now");
      };

      refresh().catch((err) => {
        updatedEl.textContent = "Unable to load progress: " + err.message;
      });
      window.setInterval(() => refresh().catch(() => {}), 2500);
    </script>
  </body>
</html>
`
