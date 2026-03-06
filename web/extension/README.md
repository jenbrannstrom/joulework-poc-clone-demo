# JouleWork POC Extension

Unpacked Chrome extension POC that:

1. Runs compute against the MCU from a designated site tab.
2. Tracks per-session joules.
3. Removes paywall overlays only after server-verified contribution reaches target joules.

## Scope (POC)

- This is not anti-abuse complete.
- Unlock is gated by server verification (`/demo/progress?sessionId=...`), not only local counters.
- Determined attackers can still reverse engineer clients in a POC setup.

## Files

- `manifest.json`: MV3 manifest and site matches.
- `content.js`: compute client + unlock gate logic.
- `compute-worker.js`: per-task compute worker.
- `popup.*`: control panel UI.

## Load In Chrome

1. Open `chrome://extensions`.
2. Enable **Developer mode**.
3. Click **Load unpacked**.
4. Select this directory: `web/extension`.

## Use

1. Open the designated site tab:
   - `http://joulework-demo.rtb.cat/`
   - or `https://jenbrannstrom.github.io/joulework-poc-clone-demo/`
2. Open extension popup.
3. Click **Start**.
4. Watch joules rise in popup (`local` and `server` values).
5. When `server >= target`, paywall overlay is removed and page unlock badge is shown.

## Designated Website Config

Change `matches` and `host_permissions` in `manifest.json` for your real site domain(s).

If you add a new domain, ensure MCU `-allow-origins` includes that origin so WebSocket upgrades are accepted.

## Notes On Backgrounding

- `Persist when tab hidden` is best effort.
- Browsers may still throttle/suspend hidden tabs.
- Full reliability requires a native helper/agent, not page JS alone.
