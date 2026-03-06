# distri-pico

A tiny distributed-compute POC for reader-funded compute.

## What This App Does (First Principles)

This app turns many small, voluntary devices (mostly browser tabs) into one temporary compute pool.

At first principles:

1. Any CPU work consumes energy over time.
2. A single reader device is small, but many readers together are meaningful.
3. Some compute jobs can be split into independent chunks.
4. A coordinator can assign chunks, collect results, and track contribution.

`distri-pico` is exactly that coordinator + worker loop.

## Mental Model

- **Coordinator (MCU server)**: keeps a queue of tasks, leases tasks to workers, validates submissions, writes results, exposes progress APIs.
- **Worker**: a browser worker or local process that asks for a task, computes it, sends result back.
- **Task chunk**: one independent unit of work from `data/chunks`.
- **Result record**: accepted output written to `data/results`.

## End-to-End Flow

1. A page loads the widget.
2. User opts in (or activates the demo button).
3. Browser opens WebSocket to MCU (`/node`).
4. Worker sends `hello`, then `request_task`.
5. MCU leases a task (with lease id + deadline).
6. Worker computes and sends `submit_result`.
7. MCU validates lease/session/payload and persists result.
8. UI polls `/demo/progress` and shows live queue + worker + PI metrics.

## Workloads Supported

- **`sha256`**: hash payload chunks.
- **`pi_leibniz`**: compute partial Leibniz series ranges and aggregate `pi` estimate.

PI snapshots are exposed via `/demo/progress` under `pi`:

- `estimate`
- `doneTerms` / `totalTerms`
- `doneTasks` / `totalTasks`

## What The Metrics Mean

From `/health` and `/demo/progress`:

- `ready`: tasks waiting in queue.
- `leased`: tasks currently checked out by workers.
- `done`: tasks completed and persisted.
- `total`: `ready + leased + done`.
- `sessions`: known worker sessions.

A value like `{"done":156,"leased":1,"ready":499,...}` means one task was actively in-flight at that instant.

## Trust And Validation (POC Level)

What is validated now:

- Message shape and required fields.
- Lease ownership (session + lease id must match).
- Lease expiry.
- Result size bounds.
- Task-specific PI result shape/range.

What is not solved yet (expected for POC):

- Strong anti-abuse / Sybil resistance.
- Cryptographic proof-of-execution.
- Economic settlement.
- Byzantine workers at internet scale.

## Canonical URL and TLS (Plain English)

- **Canonical URL** = the one official URL you treat as authoritative.
- **TLS** = the certificate + encryption system behind HTTPS.

When GitHub Pages has `https_enforced: true`, it means:

1. The custom domain has a valid TLS cert.
2. HTTP is redirected to HTTPS.
3. HTTPS becomes the canonical serving path.

If `https_enforced: false`, your custom domain may still serve primarily on HTTP.

## Quick Start (Local)

1. Seed basic hash chunks:

```bash
./scripts/seed_chunks.sh ./data/chunks 20
```

2. Or seed PI chunks:

```bash
./scripts/seed_pi_chunks.sh ./data/chunks 40 200000
```

3. Run MCU:

```bash
go run ./cmd/mcu \
  -addr :8080 \
  -chunk-dir ./data/chunks \
  -result-dir ./data/results \
  -target-joules 20
```

4. Run a worker:

```bash
go run ./cmd/local-worker -ws-url ws://127.0.0.1:8080/node?workerType=local
```

5. Serve web assets:

```bash
python3 -m http.server 9000 -d web
```

6. Open demo page:

```text
http://127.0.0.1:9000/demo/
```

## Scripts

- `scripts/seed_chunks.sh`: creates basic demo chunk files.
- `scripts/seed_pi_chunks.sh`: creates PI tasks.
  - args: `chunk_dir task_count terms_per_task [start_term] [prefix]`

Use `start_term` + `prefix` to avoid overlapping PI ranges across batches.

## Production-ish Demo Topology

- Static site: GitHub Pages (widget UI).
- Coordinator: GCP VM (MCU WebSocket/API).
- Browser clients: reader devices.

## WordPress Integration (Simple Path)

You do not need a plugin to start.

Simplest integration:

1. Load widget script in footer/header.
2. Call `window.startJouleWork({...})` with your MCU endpoint.

A plugin can come later for admin UX (on/off per page, consent copy, endpoint config).

## Repo Layout

- `cmd/mcu/main.go`: MCU server.
- `cmd/local-worker/main.go`: local worker client.
- `internal/engine/broker.go`: queue, leases, validation, persistence.
- `internal/protocol/messages.go`: protocol structs/constants.
- `web/widget/*`: embeddable browser widget + worker.
- `web/gh-pages/*`: hosted demo page assets.
- `web/extension/*`: unpacked Chrome extension POC (compute-gated paywall unlock).
- `docs/PROTOCOL.md`: wire protocol reference.

## Chrome Extension POC

A POC extension is included at `web/extension`:

- contributes compute from designated site tabs,
- verifies contribution against MCU (`/demo/progress?sessionId=...`),
- removes paywall overlays only after server-verified target joules.

Install and usage steps: `web/extension/README.md`.

## Current Public Endpoints

- Demo page: `http://joulework-demo.rtb.cat/`
- MCU health: `https://joulework-poc.rtb.cat/health`
- MCU live progress: `https://joulework-poc.rtb.cat/demo/progress`
- MCU websocket: `wss://joulework-poc.rtb.cat/node?workerType=browser`
