# JouleWork Architecture: Tiny First, Scale-Ready

Based on `BuiLD SPEC (PICOCLAW MVP).md`, this design keeps the MVP tiny while preserving upgrade paths for scale.

## Tiny Now (Build This First)

1. One Go `mcu` service on one Linux box.
2. One WebSocket endpoint: `wss://.../node`.
3. One in-memory queue plus one in-memory lease map.
4. Disk folders only: `tasks/`, `chunks/`, `results/`.
5. Local worker binary + browser WASM worker use the same protocol.
6. Picoclaw handles chunking/reassembly; MCU only schedules.

## Critical Contract To Keep Stable From Day 1

1. Chunk state machine: `ready -> leased -> done`, with lease timeout returning to `ready`.
2. Assignment includes `leaseId`; result submit must include same `leaseId`.
3. Result writes are idempotent: same `taskId` overwrite-safe or ignored if already finalized.
4. Every result includes `elapsedMs`, `workerType`, and output hash.
5. Workers always pull (`request task`) so scheduler stays simple.

If this contract stays stable, you can scale without rewriting workers.

## Scale Path (When Tiny Works)

1. Replace in-memory queue with Redis Streams or NATS JetStream.
2. Replace local disk chunks/results with S3/R2 object storage.
3. Run multiple stateless MCU instances behind one WS ingress.
4. Keep lease/ack logic in shared queue layer.
5. Add lightweight Postgres only for job metadata and observability, not core data payloads.
6. Add verification for untrusted browser work: sample dual-execution on 1-5% of chunks.

## Trust and Abuse Controls for Browser Nodes

1. Rate-limit per IP/session.
2. Cap chunk CPU time (target under 1 second).
3. Reject oversized/invalid payloads.
4. Optional signed task envelopes to prevent tampering.

## Practical Build Order

1. Implement single-node MCU + lease timeout + result persistence.
2. Ship local worker.
3. Ship browser widget/WASM with consent UI.
4. Add metrics: `queue_depth`, `lease_timeouts`, `task_latency`, `worker_disconnects`.
5. Only then move queue/storage to managed services.

## Optional Next Step

Define Go interfaces now so storage/queue backends can be swapped without changing worker protocol:

- `TaskStore`
- `LeaseStore`
- `ResultStore`
