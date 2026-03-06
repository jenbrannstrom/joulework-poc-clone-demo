# distri-pico

Tiny JouleWork POC scaffold: MCU broker, local worker, and browser widget with estimated joule progress.

## What Is Included

- Go MCU server: WebSocket broker, in-memory queue, leases, timeout requeue, result validation.
- Go local worker: requests tasks, computes SHA-256, submits result.
- Browser widget + worker: opt-in banner, active/paused/paid-share pill states.
- Protocol doc: `docs/PROTOCOL.md`.

## Repo Layout

- `cmd/mcu/main.go`: MCU server
- `cmd/local-worker/main.go`: local worker client
- `internal/engine/broker.go`: queue/lease/validation/persistence logic
- `internal/protocol/messages.go`: wire message structs and constants
- `web/widget/joulework-widget.js`: browser UI + socket logic
- `web/widget/joulework-browser-worker.js`: browser compute worker
- `web/demo/index.html`: sample page embedding widget
- `scripts/seed_chunks.sh`: creates demo chunk files

## Quick Start

1. Seed chunks:

```bash
./scripts/seed_chunks.sh ./data/chunks 20
```

2. Run MCU:

```bash
go run ./cmd/mcu \
  -addr :8080 \
  -chunk-dir ./data/chunks \
  -result-dir ./data/results \
  -target-joules 20
```

3. Run local worker:

```bash
go run ./cmd/local-worker -ws-url ws://127.0.0.1:8080/node?workerType=local
```

4. Serve `web/` statically and open demo page:

```bash
python3 -m http.server 9000 -d web
# Open http://127.0.0.1:9000/demo/
```

## Docker Quick Start (No Local Go Required)

1. Seed chunks:

```bash
./scripts/seed_chunks.sh ./data/chunks 20
```

2. Start MCU + demo web server:

```bash
cp .env.example .env
docker compose up --build mcu web-demo
```

3. Open demo page:

```text
http://127.0.0.1:9000/demo/
```

4. Optional local worker container:

```bash
docker compose --profile worker up -d local-worker
```

## Widget Behavior

- Starts with consent banner.
- On opt-in, shows active pill with tasks + estimated joules.
- If estimated joules reach target, pill turns green: `Thanks - you paid your share`.
- No background compute when tab hidden: auto-pauses on `document.hidden`.
- For shared demos, backend live progress is available at `GET /demo/progress` (queue counts, active workers, leases, recent completions).

## POC Notes

- Browser joules are estimated from elapsed time and watt assumptions.
- Local workers can later be upgraded to hardware joule telemetry.
- No durable queue or DB in this tiny version.

## Deploy Notes: GCP MCU + GH Pages Site

1. Run MCU on GCP and expose `/node` as `wss://work.yourdomain.com/node` (Cloudflare Tunnel is fine).
2. Set MCU allowed origins to your GitHub Pages origin:

```bash
ALLOW_ORIGINS=https://YOUR_GH_USER.github.io
```

3. Embed widget on GH Pages:

```html
<script src="/distri-pico/widget/joulework-widget.js"></script>
<script>
  window.startJouleWork({
    endpoint: "wss://work.yourdomain.com/node?workerType=browser",
    workerScriptUrl: "/distri-pico/widget/joulework-browser-worker.js",
    targetJoules: 20
  });
</script>
```

4. If your repo is a project site, keep the repo prefix in paths (example above: `/distri-pico/...`).
5. For local demo endpoint override, you can use:

```text
http://127.0.0.1:9000/demo/?endpoint=ws://127.0.0.1:8080/node?workerType=browser&targetJoules=20
```

## Deployed POC Targets

- GitHub repo: `https://github.com/jenbrannstrom/joulework-poc-clone-demo`
- GitHub Pages URL: `https://jenbrannstrom.github.io/joulework-poc-clone-demo/`
- Intended custom demo domain: `https://joulework-demo.rtb.cat`
- Dedicated GCP MCU static IP: kept private (look up in GCP when needed)
- Intended MCU endpoint domain: `wss://joulework-poc.rtb.cat/node?workerType=browser`

## DNS Records Needed In Cloudflare (`rtb.cat`)

1. `A` record
- Name: `joulework-poc`
- Target: `<GCP_STATIC_IP_FOR_JOULEWORK_POC_VM>`
- Proxy: DNS only (grey cloud) until TLS cert is issued

To retrieve the current static IP:

```bash
gcloud compute addresses describe joulework-poc-ip \
  --project=<PROJECT_ID> \
  --region=asia-southeast1 \
  --format='value(address)'
```

2. `CNAME` record
- Name: `joulework-demo`
- Target: `jenbrannstrom.github.io`
- Proxy: DNS only initially

After DNS propagates, Caddy on the MCU host will automatically issue TLS for `joulework-poc.rtb.cat`.
