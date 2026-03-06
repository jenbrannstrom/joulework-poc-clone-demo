package protocol

import (
	"encoding/json"
	"fmt"
)

const (
	TypeHello       = "hello"
	TypeHelloAck    = "hello_ack"
	TypeRequestTask = "request_task"
	TypeTaskAssigned = "task_assigned"
	TypeNoTask      = "no_task"
	TypeSubmitResult = "submit_result"
	TypeAck         = "ack"
	TypeError       = "error"
)

const (
	WorkerTypeBrowser = "browser"
	WorkerTypeLocal   = "local"
)

const (
	TaskTypeSHA256    = "sha256"
	TaskTypePiLeibniz = "pi_leibniz"
)

type Envelope struct {
	Type string `json:"type"`
}

type Hello struct {
	Type          string `json:"type"`
	WorkerType    string `json:"workerType"`
	SessionID     string `json:"sessionId"`
	ClientVersion string `json:"clientVersion,omitempty"`
}

type HelloAck struct {
	Type         string  `json:"type"`
	SessionID    string  `json:"sessionId"`
	TargetJoules float64 `json:"targetJoules"`
	Message      string  `json:"message,omitempty"`
}

type RequestTask struct {
	Type string `json:"type"`
}

type TaskAssigned struct {
	Type          string `json:"type"`
	TaskID        string `json:"taskId"`
	LeaseID       string `json:"leaseId"`
	TaskType      string `json:"taskType"`
	PayloadBase64 string `json:"payloadBase64"`
	DeadlineUnixMs int64 `json:"deadlineUnixMs"`
}

type NoTask struct {
	Type    string `json:"type"`
	RetryMs int    `json:"retryMs"`
}

type SubmitResult struct {
	Type       string `json:"type"`
	TaskID     string `json:"taskId"`
	LeaseID    string `json:"leaseId"`
	Result     string `json:"result"`
	ElapsedMs  int64  `json:"elapsedMs"`
	OutputHash string `json:"outputHash,omitempty"`
}

type Ack struct {
	Type            string  `json:"type"`
	TaskID          string  `json:"taskId,omitempty"`
	Accepted        bool    `json:"accepted"`
	Reason          string  `json:"reason,omitempty"`
	JoulesDeltaEst  float64 `json:"joulesDeltaEst,omitempty"`
	SessionJoulesEst float64 `json:"sessionJoulesEst,omitempty"`
	TargetJoules    float64 `json:"targetJoules,omitempty"`
	TargetReached   bool    `json:"targetReached,omitempty"`
}

type ErrorMessage struct {
	Type   string `json:"type"`
	Reason string `json:"reason"`
}

func DecodeType(payload []byte) (string, error) {
	var env Envelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return "", fmt.Errorf("decode envelope: %w", err)
	}
	if env.Type == "" {
		return "", fmt.Errorf("missing type")
	}
	return env.Type, nil
}
