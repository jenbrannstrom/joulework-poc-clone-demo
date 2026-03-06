(function () {
  const STATE_INITIAL = "initial";
  const STATE_ACTIVE = "active";
  const STATE_PAUSED = "paused";
  const STATE_PAID_SHARE = "paid_share";

  const SETTINGS_KEY = "jw_poc_extension_settings_v1";
  const SESSION_PREFIX = "jw_poc_extension_session_";
  const BADGE_ID = "jw-poc-unlocked-badge";
  const DEMO_OVERLAY_ID = "jw-poc-demo-paywall";

  const DEFAULT_CONFIG = {
    endpoint: "wss://joulework-poc.rtb.cat/node?workerType=browser",
    progressEndpoint: "https://joulework-poc.rtb.cat/demo/progress",
    targetJoules: 20,
    pauseWhenHidden: true,
    progressPollMs: 4500,
    paywallSelectors: [
      "[data-jw-paywall]",
      ".jw-paywall",
      "[data-paywall]",
      ".paywall",
      ".meter-overlay",
      ".subscribe-wall",
      ".premium-overlay",
    ],
    paywalledClassNames: ["paywalled", "is-paywalled", "has-paywall", "premium-locked"],
  };

  function randomSessionId() {
    const bytes = new Uint8Array(8);
    crypto.getRandomValues(bytes);
    return Array.from(bytes)
      .map((n) => n.toString(16).padStart(2, "0"))
      .join("");
  }

  function storageGet(keys) {
    return new Promise((resolve) => chrome.storage.local.get(keys, resolve));
  }

  function storageSet(values) {
    return new Promise((resolve) => chrome.storage.local.set(values, resolve));
  }

  function isDemoHost() {
    if (location.hostname === "joulework-demo.rtb.cat") {
      return true;
    }
    return (
      location.hostname === "jenbrannstrom.github.io" &&
      location.pathname.startsWith("/joulework-poc-clone-demo/")
    );
  }

  class JouleWorkExtensionClient {
    constructor() {
      this.config = { ...DEFAULT_CONFIG };
      this.sessionId = "";
      this.state = STATE_INITIAL;
      this.connected = false;
      this.awaitingTask = false;
      this.hasLease = false;
      this.autoPausedByVisibility = false;
      this.allowBeyondTarget = false;
      this.sessionJoules = 0;
      this.tasksCompleted = 0;
      this.unlocked = false;
      this.lastError = "";
      this.lastServerJoules = 0;
      this.lastVerifiedAt = 0;

      this.socket = null;
      this.worker = null;
      this.retryTimer = null;
      this.progressTimer = null;

      this.onVisibilityChange = this.onVisibilityChange.bind(this);
      this.onRuntimeMessage = this.onRuntimeMessage.bind(this);
    }

    sessionStorageKey() {
      return `${SESSION_PREFIX}${location.host}`;
    }

    async init() {
      await this.loadConfig();
      this.startWorker();
      this.installListeners();
      this.ensureDemoOverlay();
      this.startProgressPolling();
    }

    async loadConfig() {
      const sessionKey = this.sessionStorageKey();
      const stored = await storageGet([SETTINGS_KEY, sessionKey]);
      const savedSettings = stored[SETTINGS_KEY] || {};
      this.config = { ...DEFAULT_CONFIG, ...savedSettings };

      if (stored[sessionKey]) {
        this.sessionId = String(stored[sessionKey]);
      } else {
        this.sessionId = randomSessionId();
        await storageSet({ [sessionKey]: this.sessionId });
      }
    }

    async saveConfig(patch) {
      this.config = { ...this.config, ...patch };
      const current = await storageGet([SETTINGS_KEY]);
      const merged = { ...(current[SETTINGS_KEY] || {}), ...patch };
      await storageSet({ [SETTINGS_KEY]: merged });
    }

    installListeners() {
      document.addEventListener("visibilitychange", this.onVisibilityChange);
      chrome.runtime.onMessage.addListener(this.onRuntimeMessage);
    }

    destroy() {
      document.removeEventListener("visibilitychange", this.onVisibilityChange);
      chrome.runtime.onMessage.removeListener(this.onRuntimeMessage);
      this.clearRetry();
      if (this.progressTimer) {
        clearInterval(this.progressTimer);
        this.progressTimer = null;
      }
      if (this.socket && this.socket.readyState <= WebSocket.OPEN) {
        this.socket.close();
      }
      if (this.worker) {
        this.worker.terminate();
      }
    }

    onRuntimeMessage(message, _sender, sendResponse) {
      if (!message || typeof message.type !== "string") {
        return;
      }

      this.handleRuntimeMessage(message)
        .then((response) => sendResponse(response))
        .catch((error) => {
          this.lastError = error instanceof Error ? error.message : String(error);
          sendResponse(this.statusPayload(false));
        });
      return true;
    }

    async handleRuntimeMessage(message) {
      switch (message.type) {
        case "jw_get_status":
          return this.statusPayload(true);
        case "jw_start":
          this.start();
          return this.statusPayload(true);
        case "jw_pause":
          this.pause();
          return this.statusPayload(true);
        case "jw_resume":
          this.resume();
          return this.statusPayload(true);
        case "jw_set_persist_hidden": {
          const persist = Boolean(message.value);
          await this.saveConfig({ pauseWhenHidden: !persist });
          if (!this.config.pauseWhenHidden) {
            this.autoPausedByVisibility = false;
          }
          return this.statusPayload(true);
        }
        case "jw_unlock_now":
          await this.verifyAndUnlock();
          return this.statusPayload(true);
        default:
          return { ok: false, error: "unknown message type" };
      }
    }

    startWorker() {
      if (this.worker) {
        return;
      }
      this.worker = new Worker(chrome.runtime.getURL("compute-worker.js"));
      this.worker.onmessage = (event) => {
        const message = event.data || {};
        if (message.type === "compute_error") {
          this.hasLease = false;
          this.scheduleRetry(700);
          return;
        }
        if (message.type !== "computed") {
          return;
        }
        this.send({
          type: "submit_result",
          taskId: message.taskId,
          leaseId: message.leaseId,
          result: message.result,
          elapsedMs: message.elapsedMs,
          outputHash: message.outputHash,
        });
      };
    }

    start() {
      if (this.state === STATE_ACTIVE) {
        return;
      }
      this.lastError = "";
      this.state = STATE_ACTIVE;
      this.connectSocket();
      this.requestTask();
    }

    pause() {
      if (this.state !== STATE_ACTIVE && this.state !== STATE_PAID_SHARE) {
        return;
      }
      this.state = STATE_PAUSED;
      this.awaitingTask = false;
      this.hasLease = false;
      this.clearRetry();
    }

    resume() {
      if (this.state !== STATE_PAUSED && this.state !== STATE_PAID_SHARE) {
        return;
      }
      if (this.state === STATE_PAID_SHARE) {
        this.allowBeyondTarget = true;
      }
      this.state = STATE_ACTIVE;
      this.connectSocket();
      this.requestTask();
    }

    connectSocket() {
      if (this.connected || (this.socket && this.socket.readyState === WebSocket.CONNECTING)) {
        return;
      }

      try {
        this.socket = new WebSocket(this.config.endpoint);
      } catch (error) {
        this.lastError = error instanceof Error ? error.message : "socket init failed";
        this.scheduleRetry(1500);
        return;
      }

      this.socket.onopen = () => {
        this.connected = true;
        this.send({
          type: "hello",
          workerType: "browser",
          sessionId: this.sessionId,
          clientVersion: "ext-poc-0.1.0",
        });
      };

      this.socket.onmessage = (event) => {
        let payload;
        try {
          payload = JSON.parse(event.data);
        } catch (_error) {
          return;
        }
        this.handleSocketMessage(payload);
      };

      this.socket.onclose = () => {
        this.connected = false;
        this.awaitingTask = false;
        this.hasLease = false;
        if (this.state === STATE_ACTIVE) {
          this.scheduleRetry(1500);
        }
      };

      this.socket.onerror = () => {
        if (this.socket) {
          this.socket.close();
        }
      };
    }

    handleSocketMessage(payload) {
      if (!payload || typeof payload.type !== "string") {
        return;
      }

      if (payload.type === "hello_ack") {
        if (payload.sessionId) {
          this.sessionId = String(payload.sessionId);
          storageSet({ [this.sessionStorageKey()]: this.sessionId });
        }
        if (typeof payload.targetJoules === "number" && payload.targetJoules > 0) {
          this.config.targetJoules = payload.targetJoules;
        }
        if (this.state === STATE_ACTIVE) {
          this.requestTask();
        }
        return;
      }

      if (payload.type === "no_task") {
        this.awaitingTask = false;
        if (this.state === STATE_ACTIVE) {
          this.scheduleRetry(Number(payload.retryMs || 1200));
        }
        return;
      }

      if (payload.type === "task_assigned") {
        this.awaitingTask = false;
        this.hasLease = true;
        if (this.state !== STATE_ACTIVE || !this.worker) {
          return;
        }
        this.worker.postMessage({
          type: "compute",
          taskId: payload.taskId,
          leaseId: payload.leaseId,
          taskType: payload.taskType || "sha256",
          payloadBase64: payload.payloadBase64,
        });
        return;
      }

      if (payload.type === "ack") {
        this.hasLease = false;
        if (payload.accepted) {
          this.tasksCompleted += 1;
          if (typeof payload.sessionJoulesEst === "number") {
            this.sessionJoules = payload.sessionJoulesEst;
          } else if (typeof payload.joulesDeltaEst === "number") {
            this.sessionJoules += payload.joulesDeltaEst;
          }
          if (!this.allowBeyondTarget && (payload.targetReached || this.sessionJoules >= this.config.targetJoules)) {
            this.state = STATE_PAID_SHARE;
          }
        }

        if (this.state === STATE_ACTIVE) {
          this.requestTask();
        }
      }
    }

    requestTask() {
      if (!this.connected || this.state !== STATE_ACTIVE || this.awaitingTask || this.hasLease) {
        return;
      }
      this.awaitingTask = true;
      this.send({ type: "request_task" });
    }

    scheduleRetry(ms) {
      this.clearRetry();
      this.retryTimer = window.setTimeout(() => {
        if (this.state !== STATE_ACTIVE) {
          return;
        }
        this.connectSocket();
        this.requestTask();
      }, ms);
    }

    clearRetry() {
      if (this.retryTimer) {
        clearTimeout(this.retryTimer);
        this.retryTimer = null;
      }
    }

    send(payload) {
      if (!this.socket || this.socket.readyState !== WebSocket.OPEN) {
        return;
      }
      this.socket.send(JSON.stringify(payload));
    }

    onVisibilityChange() {
      if (document.hidden) {
        if (this.config.pauseWhenHidden && this.state === STATE_ACTIVE) {
          this.autoPausedByVisibility = true;
          this.pause();
        }
        return;
      }

      if (!document.hidden && this.state === STATE_PAUSED && this.autoPausedByVisibility) {
        this.autoPausedByVisibility = false;
        this.resume();
      }
    }

    startProgressPolling() {
      if (this.progressTimer) {
        clearInterval(this.progressTimer);
      }
      this.progressTimer = window.setInterval(() => {
        if (this.state === STATE_ACTIVE || this.state === STATE_PAID_SHARE) {
          this.verifyAndUnlock().catch((error) => {
            this.lastError = error instanceof Error ? error.message : String(error);
          });
        }
      }, Math.max(1000, Number(this.config.progressPollMs || 4500)));
    }

    async verifyAndUnlock() {
      if (!this.sessionId) {
        return false;
      }

      const separator = this.config.progressEndpoint.includes("?") ? "&" : "?";
      const url = `${this.config.progressEndpoint}${separator}sessionId=${encodeURIComponent(this.sessionId)}`;
      const response = await fetch(url, { cache: "no-store" });
      if (!response.ok) {
        throw new Error(`progress HTTP ${response.status}`);
      }
      const payload = await response.json();
      const joules = Number((payload.mySession || {}).joulesEst || 0);
      this.lastServerJoules = joules;
      this.lastVerifiedAt = Date.now();

      if (joules >= Number(this.config.targetJoules || 20)) {
        this.unlockPage();
        this.unlocked = true;
        return true;
      }
      return false;
    }

    ensureDemoOverlay() {
      if (!isDemoHost()) {
        return;
      }
      const alreadyPresent = this.config.paywallSelectors.some((selector) => document.querySelector(selector));
      if (alreadyPresent) {
        return;
      }

      if (document.getElementById(DEMO_OVERLAY_ID)) {
        return;
      }

      const overlay = document.createElement("div");
      overlay.id = DEMO_OVERLAY_ID;
      overlay.setAttribute("data-jw-paywall", "1");
      overlay.style.position = "fixed";
      overlay.style.inset = "0";
      overlay.style.zIndex = "999999";
      overlay.style.background = "rgba(6, 10, 14, 0.78)";
      overlay.style.backdropFilter = "blur(3px)";
      overlay.style.display = "grid";
      overlay.style.placeItems = "center";
      overlay.innerHTML = [
        '<div style="max-width:360px;padding:16px 18px;border-radius:12px;border:1px solid #49677d;background:#101b25;color:#eaf7ff;font:14px/1.4 ui-sans-serif,system-ui;">',
        "<strong>Reader compute paywall (POC)</strong><br/>",
        "Start contribution from the extension popup and reach your target joules to unlock.",
        "</div>",
      ].join("");
      document.body.appendChild(overlay);
      document.documentElement.style.overflow = "hidden";
      document.body.style.overflow = "hidden";
    }

    unlockPage() {
      const selectors = Array.isArray(this.config.paywallSelectors) ? this.config.paywallSelectors : [];
      selectors.forEach((selector) => {
        document.querySelectorAll(selector).forEach((node) => {
          node.style.setProperty("display", "none", "important");
          node.setAttribute("data-jw-hidden", "1");
        });
      });

      const paywallClasses = Array.isArray(this.config.paywalledClassNames) ? this.config.paywalledClassNames : [];
      paywallClasses.forEach((className) => {
        document.documentElement.classList.remove(className);
        document.body.classList.remove(className);
      });

      if (document.getElementById(DEMO_OVERLAY_ID)) {
        document.getElementById(DEMO_OVERLAY_ID).remove();
      }

      document.documentElement.style.overflow = "";
      document.body.style.overflow = "";
      this.renderUnlockedBadge();
    }

    renderUnlockedBadge() {
      if (document.getElementById(BADGE_ID)) {
        return;
      }
      const el = document.createElement("div");
      el.id = BADGE_ID;
      el.textContent = "Unlocked by JouleWork compute (POC)";
      el.style.position = "fixed";
      el.style.right = "14px";
      el.style.bottom = "14px";
      el.style.zIndex = "999999";
      el.style.padding = "8px 10px";
      el.style.borderRadius = "999px";
      el.style.border = "1px solid #2d5e31";
      el.style.background = "#d8f5da";
      el.style.color = "#134116";
      el.style.font = "700 12px/1.2 ui-sans-serif,system-ui";
      document.body.appendChild(el);
    }

    statusPayload(ok) {
      return {
        ok,
        state: this.state,
        connected: this.connected,
        unlocked: this.unlocked,
        sessionId: this.sessionId,
        sessionJoules: this.sessionJoules,
        serverJoules: this.lastServerJoules,
        targetJoules: this.config.targetJoules,
        tasksCompleted: this.tasksCompleted,
        pauseWhenHidden: this.config.pauseWhenHidden,
        lastVerifiedAt: this.lastVerifiedAt,
        lastError: this.lastError,
      };
    }
  }

  const client = new JouleWorkExtensionClient();
  client.init().catch((error) => {
    console.error("JouleWork extension init failed:", error);
  });
})();
