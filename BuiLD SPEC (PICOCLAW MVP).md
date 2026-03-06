**⚡ JOULEWORK — BUILD SPEC (PICOCLAW MVP)**

---

## What We Are Building

A distributed compute network. The master (MCU) runs Picoclaw on a cheap Linux box. It delegates work to:

- Local computers on the same network (fast, trusted)  
- Browser tabs of website visitors (slow, opportunistic)

The browser nodes are slower. That's fine. The system doesn't care — it sends tasks to whatever is available and collects results.

---

## The Core Loop (First Principles)

1. **Work enters the system** — Files appear in a folder on the MCU.  
2. **Work gets chunked** — Picoclaw splits big jobs into small pieces.  
3. **Nodes ask for work** — Any connected computer or browser that wants to help requests a task.  
4. **MCU hands out chunks** — One task per node, immediately.  
5. **Nodes compute** — They process the chunk and send back the result.  
6. **MCU saves result** — Completed chunks go into an output folder.  
7. **Repeat** — When a node finishes one chunk, it gets the next.

That's it. No queue persistence. No database. No accounts. The completed files in the output folder are the proof that work happened.

---

## Components

### 1\. The MCU (Master Control Unit)

**Hardware:** Anything that runs Linux — old laptop, Raspberry Pi, cheap VPS.  
**Software:** Picoclaw (or a custom Go orchestrator if Picoclaw isn't ready).

**What it does:**

- Watches a `tasks/` folder for new work.  
- Breaks work into chunks (if needed).  
- Maintains a list of connected nodes (WebSocket).  
- Hands out chunks immediately when a node connects or finishes.  
- Saves results to a `results/` folder.  
- Forgets everything else — no storage of completed tasks, no ledger.

**Why no database:**  
The finished files *are* the database. If you need a record later, you scan the results folder. This is simpler and matches the physics-grounded idea — the completed work is the payment, not a token representing it.

---

### 2\. Local Nodes (Fast Workers)

**Hardware:** Any computer on the same network as the MCU — old desktops, laptops, SBCs.  
**Connection:** WebSocket to the MCU's local IP (or over Tailscale if not on same subnet).

**What they do:**

- Run a small Go binary (compiled for their OS/arch).  
- Connect to the MCU and stay connected 24/7 if possible.  
- Request work, process it, send back results.  
- Repeat until the MCU has no more tasks.

**Speed:** These are the baseline. They're always faster than browser nodes. The MCU will prioritise them simply because they ask for work more often.

---

### 3\. Browser Nodes (Slow, Opportunistic Workers)

**Hardware:** Any device with a web browser — mostly desktops, some laptops.  
**Software:** A JavaScript widget \+ WebAssembly worker.

**How a reader becomes a node:**

1. They visit a webpage with the JouleWork widget installed.  
2. After 1–2 seconds, a banner appears:  
   *"Help the author — donate idle CPU while you read."*  
3. They click YES.  
4. The browser downloads a small WASM worker (same Go code as local nodes, compiled to wasm).  
5. The worker opens a WebSocket to the MCU (via Cloudflare Tunnel or direct).  
6. It starts requesting and processing tasks.  
7. A small UI pill shows: `⚡ Contributing · 12 tasks`

**When they stop:**

- Click the pill → pause.  
- Close the tab → stop completely.  
- Navigate away → stop.

No service workers. No background compute. If the tab isn't visible and focused on the content, no work happens.

**Why they're slower:**

- WASM in a browser is slower than native.  
- Browsers throttle background tabs.  
- Network latency is higher.  
- They disconnect unpredictably.

**This is acceptable** because:

- The MCU treats all nodes equally — it just hands out chunks.  
- If a browser disconnects mid-task, the chunk times out and goes back to the queue.  
- More readers \= more slow nodes \= more total throughput. A popular article with 500 readers active for 5 minutes each moves a lot of work.

---

## Task Flow (Step by Step)

**Assumption:** Picoclaw handles chunking and result assembly. The MCU just moves chunks.

1. **User uploads a job**  
   They drop a file (or folder) into `tasks/`.  
   Picoclaw detects it, splits it into chunks (e.g., 1MB pieces), and writes them to `chunks/`.  
     
2. **MCU sees chunks**  
   It scans `chunks/` periodically. Any chunk not yet assigned goes into the in-memory queue.  
     
3. **Node connects**  
   A local computer or browser tab opens a WebSocket to the MCU.  
     
4. **Node requests work**  
   It sends: `{ "request": "task" }`  
     
5. **MCU assigns a chunk**  
   It pops the next chunk from the queue and sends:  
   `{ "taskId": "chunk_042", "data": "...", "type": "ocr" }`  
     
6. **Node processes**  
     
   - If it's a browser: WASM runs SHA-256 or OCR or whatever the task is.  
   - If it's a local node: native binary does the same work, faster.

   

7. **Node returns result**  
   `{ "taskId": "chunk_042", "result": "...", "elapsedMs": 147 }`  
     
8. **MCU saves result**  
   Writes `chunk_042.result` to `results/`.  
   Picoclaw (or a separate assembler) later combines all results into the final output.  
     
9. **Repeat**  
   MCU immediately sends the next chunk to the same node.  
     
10. **Node disconnects**  
    If a browser tab closes mid-task, that chunk never gets a result. After a timeout (e.g., 30 seconds), the MCU puts it back in the queue. Another node will pick it up.

---

## What the Browser Widget Shows the Reader

Three states. No confusion.

| State | UI | What's happening |
| :---- | :---- | :---- |
| **Initial** | Small banner: "Support this author — donate idle CPU?" with YES button and "No thanks" link. | Nothing runs. No consent yet. |
| **Active** | Green pill in corner: "● Contributing · 14 tasks" — click to pause. | WASM worker running. Tasks being processed. |
| **Paused** | Grey pill: "○ Paused · click to resume" | Worker suspended. Can resume anytime. |
| **Closing tab** | Browser dialog (optional): "You're helping process work. Leave tab open to keep contributing." | Reminds reader they're actively helping. No force. |

---

## Technical Limits (Deliberate)

**No service workers**  
If compute continues after tab close, the reader loses visibility and control. That's the line we don't cross. Coinhive (2018) ran in background and destroyed trust in browser compute. JouleWork is transparent by design — visible tab \= compute, closed tab \= stop.

**No persistence in browser**  
No localStorage. No IndexedDB. No way to track readers across sessions. Each visit is fresh. The only identifier is a random session ID stored in memory, lost on tab close.

**No authentication**  
Nodes don't log in. The MCU doesn't know who they are. For the POC, we don't need to attribute work to specific readers. If we later add Joule credits, we'll add anonymous session tokens that persist via cookies (opt-in).

**No mobile (yet)**  
iOS throttles JS in background tabs aggressively. Mobile Safari kills timers after \~30 seconds in background. Mobile nodes are unreliable — we ignore them for POC and target desktop readers.

---

## What Picoclaw Must Handle

If Picoclaw is the work manager, it needs to:

1. **Chunk input** — Split large files into pieces small enough for a browser to process in \<1 second.  
2. **Queue chunks** — Make them available to the MCU for distribution.  
3. **Reassemble results** — Combine completed chunks into final output.  
4. **Retry failed chunks** — If a chunk times out, make it available again.

The MCU (our orchestrator) is just a dumb pipe:  
`chunks/` → queue → nodes → `results/`

Picoclaw owns the intelligence: what tasks to run, how to split them, how to validate results, and how to combine them.

---

## Infrastructure (Free Tier)

- **MCU host:** GCP e2-micro (free) or an old laptop at home.  
- **Connection:** Cloudflare Tunnel (free) — gives a public `wss://` URL without opening ports.  
- **Browser nodes:** Connect to `wss://work.yourdomain.com/node`  
- **Local nodes:** Connect directly to MCU's LAN IP (or via Tailscale).

**Data flow:**

Browser → Cloudflare Edge → Cloudflare Tunnel → MCU (localhost:8080)

Local computer → (Tailscale/LAN) → MCU (192.168.1.x:8080)

MCU never needs a public IP. Cloudflare handles TLS and routing.

---

## What Success Looks Like (POC)

1. Picoclaw runs on the MCU, watching `tasks/`.  
2. A writer drops a scanned PDF into `tasks/`.  
3. Picoclaw chunks it into 50 pieces, writes them to `chunks/`.  
4. MCU starts handing chunks to:  
   - 3 always-on local computers in Brian's garage  
   - 12 browser tabs from readers on the website  
5. Over 10 minutes, all 50 chunks complete.  
6. Picoclaw reassembles them into `final_output.txt`.  
7. Writer has a transcribed document. Readers contributed compute while reading. No money changed hands. No ads. No tracking.

---

## One-Sentence Summary

JouleWork is a WebSocket broker that takes chunks of work from Picoclaw and hands them to any connected node — local computers or browser tabs — saving results back to disk, with no database, no accounts, and no background compute after tab close.  
