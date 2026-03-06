package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/joulework/distri-pico/internal/protocol"
)

type piTaskPayload struct {
	TaskType  string `json:"taskType"`
	StartTerm int64  `json:"startTerm"`
	TermCount int64  `json:"termCount"`
}

type piPartialResult struct {
	Kind       string  `json:"kind"`
	StartTerm  int64   `json:"startTerm"`
	TermCount  int64   `json:"termCount"`
	PartialSum float64 `json:"partialSum"`
}

func main() {
	var (
		wsURL      = flag.String("ws-url", "ws://127.0.0.1:8080/node?workerType=local", "MCU websocket URL")
		sessionID  = flag.String("session-id", "", "Worker session ID")
		workerType = flag.String("worker-type", protocol.WorkerTypeLocal, "Worker type: local|browser")
		retryMs    = flag.Int("retry-ms", 1000, "Retry delay when no task is available")
	)
	flag.Parse()

	if *sessionID == "" {
		*sessionID = randomID(8)
	}

	for {
		if err := runWorker(*wsURL, *sessionID, normalizeWorkerType(*workerType), *retryMs); err != nil {
			log.Printf("worker loop error: %v", err)
			time.Sleep(2 * time.Second)
		}
	}
}

func runWorker(wsURL, sessionID, workerType string, defaultRetryMs int) error {
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return fmt.Errorf("dial websocket: %w", err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(protocol.Hello{
		Type:       protocol.TypeHello,
		WorkerType: workerType,
		SessionID:  sessionID,
	}); err != nil {
		return fmt.Errorf("send hello: %w", err)
	}

	if _, payload, err := conn.ReadMessage(); err == nil {
		if messageType, _ := protocol.DecodeType(payload); messageType == protocol.TypeHelloAck {
			var ack protocol.HelloAck
			_ = json.Unmarshal(payload, &ack)
			sessionID = ack.SessionID
			log.Printf("connected session=%s targetJ=%.2f", sessionID, ack.TargetJoules)
		}
	} else {
		return fmt.Errorf("read hello ack: %w", err)
	}

	for {
		if err := conn.WriteJSON(protocol.RequestTask{Type: protocol.TypeRequestTask}); err != nil {
			return fmt.Errorf("request task: %w", err)
		}

		_, payload, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read message: %w", err)
		}
		msgType, err := protocol.DecodeType(payload)
		if err != nil {
			log.Printf("ignore invalid message: %v", err)
			continue
		}

		switch msgType {
		case protocol.TypeNoTask:
			var noTask protocol.NoTask
			if err := json.Unmarshal(payload, &noTask); err != nil {
				log.Printf("invalid no_task payload: %v", err)
				time.Sleep(time.Duration(defaultRetryMs) * time.Millisecond)
				continue
			}
			retry := noTask.RetryMs
			if retry <= 0 {
				retry = defaultRetryMs
			}
			time.Sleep(time.Duration(retry) * time.Millisecond)
		case protocol.TypeTaskAssigned:
			var task protocol.TaskAssigned
			if err := json.Unmarshal(payload, &task); err != nil {
				log.Printf("invalid task_assigned payload: %v", err)
				continue
			}
			if err := processTask(conn, task); err != nil {
				log.Printf("process task %s failed: %v", task.TaskID, err)
			}
		case protocol.TypeError:
			var msg protocol.ErrorMessage
			if err := json.Unmarshal(payload, &msg); err == nil {
				log.Printf("server error: %s", msg.Reason)
			} else {
				log.Printf("server error")
			}
		default:
			log.Printf("ignore unsupported message type: %s", msgType)
		}
	}
}

func processTask(conn *websocket.Conn, task protocol.TaskAssigned) error {
	payload, err := base64.StdEncoding.DecodeString(task.PayloadBase64)
	if err != nil {
		return fmt.Errorf("decode payload: %w", err)
	}
	resultValue, outputHash, elapsed, err := computeTaskResult(task.TaskType, payload)
	if err != nil {
		return err
	}

	submit := protocol.SubmitResult{
		Type:       protocol.TypeSubmitResult,
		TaskID:     task.TaskID,
		LeaseID:    task.LeaseID,
		Result:     resultValue,
		ElapsedMs:  elapsed,
		OutputHash: outputHash,
	}
	if err := conn.WriteJSON(submit); err != nil {
		return fmt.Errorf("submit result: %w", err)
	}

	_, ackPayload, err := conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("read ack: %w", err)
	}
	msgType, err := protocol.DecodeType(ackPayload)
	if err != nil {
		return fmt.Errorf("decode ack type: %w", err)
	}
	if msgType == protocol.TypeError {
		var msg protocol.ErrorMessage
		if err := json.Unmarshal(ackPayload, &msg); err == nil {
			return fmt.Errorf("server error: %s", msg.Reason)
		}
		return fmt.Errorf("server error")
	}
	if msgType != protocol.TypeAck {
		return fmt.Errorf("expected ack, got %s", msgType)
	}

	var ack protocol.Ack
	if err := json.Unmarshal(ackPayload, &ack); err != nil {
		return fmt.Errorf("decode ack: %w", err)
	}
	if !ack.Accepted {
		return fmt.Errorf("task rejected: %s", ack.Reason)
	}
	log.Printf("task=%s accepted sessionJ=%.2f targetReached=%v", task.TaskID, ack.SessionJoulesEst, ack.TargetReached)
	return nil
}

func computeTaskResult(taskType string, payload []byte) (string, string, int64, error) {
	start := time.Now()
	switch taskType {
	case protocol.TaskTypePiLeibniz:
		var task piTaskPayload
		if err := json.Unmarshal(payload, &task); err != nil {
			return "", "", 0, fmt.Errorf("decode pi payload: %w", err)
		}
		if task.StartTerm < 0 || task.TermCount <= 0 {
			return "", "", 0, fmt.Errorf("invalid pi payload bounds")
		}
		partial := 0.0
		end := task.StartTerm + task.TermCount
		for k := task.StartTerm; k < end; k++ {
			sign := 1.0
			if k%2 == 1 {
				sign = -1.0
			}
			partial += sign / float64(2*k+1)
		}
		resultData, err := json.Marshal(piPartialResult{
			Kind:       "pi_leibniz_partial",
			StartTerm:  task.StartTerm,
			TermCount:  task.TermCount,
			PartialSum: partial,
		})
		if err != nil {
			return "", "", 0, fmt.Errorf("encode pi result: %w", err)
		}
		elapsed := time.Since(start).Milliseconds()
		if elapsed <= 0 {
			elapsed = 1
		}
		return string(resultData), "", elapsed, nil
	default:
		sum := sha256.Sum256(payload)
		resultHash := hex.EncodeToString(sum[:])
		elapsed := time.Since(start).Milliseconds()
		if elapsed <= 0 {
			elapsed = 1
		}
		return resultHash, resultHash, elapsed, nil
	}
}

func normalizeWorkerType(workerType string) string {
	if strings.EqualFold(workerType, protocol.WorkerTypeBrowser) {
		return protocol.WorkerTypeBrowser
	}
	return protocol.WorkerTypeLocal
}

func randomID(nBytes int) string {
	buf := make([]byte, nBytes)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return hex.EncodeToString(buf)
}
