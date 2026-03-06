# JouleWork POC Protocol (MCU <-> Worker)

All messages are JSON over one WebSocket connection.

## Worker -> MCU

### `hello`

```json
{
  "type": "hello",
  "workerType": "browser",
  "sessionId": "a1b2c3d4e5f6",
  "clientVersion": "0.1.0"
}
```

### `request_task`

```json
{
  "type": "request_task"
}
```

### `submit_result`

```json
{
  "type": "submit_result",
  "taskId": "chunk_001.txt",
  "leaseId": "4dd3e4f1f2ab...",
  "result": "<task output>",
  "elapsedMs": 147,
  "outputHash": "<sha256-hex>"
}
```

## MCU -> Worker

### `hello_ack`

```json
{
  "type": "hello_ack",
  "sessionId": "a1b2c3d4e5f6",
  "targetJoules": 20
}
```

### `task_assigned`

```json
{
  "type": "task_assigned",
  "taskId": "chunk_001.txt",
  "leaseId": "4dd3e4f1f2ab...",
  "taskType": "sha256",
  "payloadBase64": "...",
  "deadlineUnixMs": 1770000123456
}
```

### `no_task`

```json
{
  "type": "no_task",
  "retryMs": 1000
}
```

### `ack`

```json
{
  "type": "ack",
  "taskId": "chunk_001.txt",
  "accepted": true,
  "joulesDeltaEst": 1.76,
  "sessionJoulesEst": 8.4,
  "targetJoules": 20,
  "targetReached": false
}
```

### `error`

```json
{
  "type": "error",
  "reason": "invalid envelope"
}
```

## Validation Rules (POC)

1. `taskId` and `leaseId` required on submit.
2. Active lease must exist and match `leaseId`.
3. Submitter session must match lease owner session.
4. `elapsedMs` must be in range.
5. Result payload must be under configured max bytes.
6. Optional `outputHash` must be valid SHA-256 hex if present.
7. Duplicate done tasks are idempotently accepted as ignored.
