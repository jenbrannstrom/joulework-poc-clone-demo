(function () {
  const STATE_INITIAL = "initial";
  const STATE_ACTIVE = "active";
  const STATE_PAUSED = "paused";
  const STATE_PAID_SHARE = "paid_share";
  const STATE_DISMISSED = "dismissed";
  const CURRENT_SCRIPT_SRC =
    document.currentScript && document.currentScript.src ? document.currentScript.src : window.location.href;

  function randomSessionId() {
    const bytes = new Uint8Array(8);
    crypto.getRandomValues(bytes);
    return Array.from(bytes)
      .map((n) => n.toString(16).padStart(2, "0"))
      .join("");
  }

  function defaultEndpoint() {
    const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
    return `${protocol}//${window.location.host}/node?workerType=browser`;
  }

  function injectBaseStyles() {
    if (document.getElementById("joulework-styles")) {
      return;
    }
    const style = document.createElement("style");
    style.id = "joulework-styles";
    style.textContent = `
      .jw-banner {
        position: fixed;
        bottom: 18px;
        right: 18px;
        z-index: 99999;
        max-width: 300px;
        background: #111827;
        color: #f3f4f6;
        border-radius: 12px;
        padding: 12px;
        box-shadow: 0 8px 24px rgba(0, 0, 0, 0.28);
        font-family: ui-sans-serif, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
      }
      .jw-banner h4 {
        margin: 0 0 8px;
        font-size: 15px;
      }
      .jw-banner p {
        margin: 0 0 10px;
        font-size: 13px;
        line-height: 1.4;
      }
      .jw-row {
        display: flex;
        gap: 8px;
      }
      .jw-btn {
        border: 0;
        border-radius: 999px;
        padding: 8px 12px;
        font-size: 12px;
        cursor: pointer;
      }
      .jw-btn-yes {
        background: #22c55e;
        color: white;
      }
      .jw-btn-no {
        background: #374151;
        color: #d1d5db;
      }
      .jw-pill {
        position: fixed;
        bottom: 18px;
        right: 18px;
        z-index: 99999;
        border: 0;
        border-radius: 999px;
        padding: 10px 14px;
        font-size: 12px;
        font-family: ui-sans-serif, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
        cursor: pointer;
        box-shadow: 0 8px 20px rgba(0, 0, 0, 0.2);
      }
      .jw-pill.active {
        background: #166534;
        color: #ecfdf5;
      }
      .jw-pill.paused {
        background: #4b5563;
        color: #f3f4f6;
      }
      .jw-pill.paid {
        background: #22c55e;
        color: #052e16;
      }
    `;
    document.head.appendChild(style);
  }

  class JouleWorkWidget {
    constructor(config = {}) {
      this.config = {
        endpoint: config.endpoint || defaultEndpoint(),
        targetJoules: Number(config.targetJoules || 20),
        estimatedBrowserWatts: Number(config.estimatedBrowserWatts || 12),
        showAfterMs: Number(config.showAfterMs || 1200),
        showConsentBanner: config.showConsentBanner !== false,
        showPill: config.showPill !== false,
        pauseWhenHidden: config.pauseWhenHidden !== false,
        workerScriptUrl:
          config.workerScriptUrl ||
          new URL("./joulework-browser-worker.js", config.baseScriptUrl || CURRENT_SCRIPT_SRC).toString(),
      };

      this.state = STATE_INITIAL;
      this.sessionId = config.sessionId || randomSessionId();
      this.sessionJoules = 0;
      this.tasksCompleted = 0;
      this.awaitingTask = false;
      this.hasLease = false;
      this.connected = false;
      this.autoPausedByVisibility = false;
      this.retryTimer = null;
      this.manualStop = false;
      this.lastElapsedMs = 0;
      this.allowBeyondTarget = false;

      this.socket = null;
      this.worker = null;
      this.bannerEl = null;
      this.pillEl = null;

      this.onVisibilityChange = this.onVisibilityChange.bind(this);
      this.onBeforeUnload = this.onBeforeUnload.bind(this);
    }

    mount() {
      injectBaseStyles();
      if (this.config.showConsentBanner) {
        window.setTimeout(() => {
          if (this.state === STATE_INITIAL) {
            this.renderBanner();
          }
        }, this.config.showAfterMs);
      }

      document.addEventListener("visibilitychange", this.onVisibilityChange);
      window.addEventListener("beforeunload", this.onBeforeUnload);
    }

    renderBanner() {
      if (this.bannerEl || this.state !== STATE_INITIAL) {
        return;
      }
      const root = document.createElement("div");
      root.className = "jw-banner";
      root.innerHTML = `
        <h4>Support this author</h4>
        <p>Donate idle CPU while you read. No background compute after tab close.</p>
        <div class="jw-row">
          <button class="jw-btn jw-btn-yes" type="button">Yes</button>
          <button class="jw-btn jw-btn-no" type="button">No thanks</button>
        </div>
      `;

      root.querySelector(".jw-btn-yes").addEventListener("click", () => this.activate());
      root.querySelector(".jw-btn-no").addEventListener("click", () => this.dismiss());
      document.body.appendChild(root);
      this.bannerEl = root;
    }

    renderPill() {
      if (!this.config.showPill) {
        return;
      }
      if (!this.pillEl) {
        const pill = document.createElement("button");
        pill.type = "button";
        pill.className = "jw-pill";
        pill.addEventListener("click", () => this.togglePillAction());
        document.body.appendChild(pill);
        this.pillEl = pill;
      }

      const target = this.config.targetJoules.toFixed(1);
      const joules = this.sessionJoules.toFixed(1);
      if (this.state === STATE_ACTIVE) {
        this.pillEl.className = "jw-pill active";
        this.pillEl.textContent = `ON Contributing - ${this.tasksCompleted} tasks - ${joules}J / ${target}J`;
      } else if (this.state === STATE_PAUSED) {
        this.pillEl.className = "jw-pill paused";
        this.pillEl.textContent = `PAUSED - ${joules}J - click to resume`;
      } else if (this.state === STATE_PAID_SHARE) {
        this.pillEl.className = "jw-pill paid";
        this.pillEl.textContent = `DONE Thanks - you paid your share (${joules}J)`;
      }
    }

    activate() {
      if (this.bannerEl) {
        this.bannerEl.remove();
        this.bannerEl = null;
      }
      this.state = STATE_ACTIVE;
      this.renderPill();
      this.startWorker();
      this.connectSocket();
    }

    dismiss() {
      this.state = STATE_DISMISSED;
      if (this.bannerEl) {
        this.bannerEl.remove();
        this.bannerEl = null;
      }
    }

    pause() {
      if (this.state !== STATE_ACTIVE && this.state !== STATE_PAID_SHARE) {
        return;
      }
      this.state = STATE_PAUSED;
      this.awaitingTask = false;
      this.clearRetry();
      this.renderPill();
    }

    resume() {
      if (this.state !== STATE_PAUSED && this.state !== STATE_PAID_SHARE) {
        return;
      }
      if (this.state === STATE_PAID_SHARE) {
        this.allowBeyondTarget = true;
      }
      this.state = STATE_ACTIVE;
      this.renderPill();
      if (!this.connected) {
        this.connectSocket();
        return;
      }
      this.requestTask();
    }

    togglePillAction() {
      if (this.state === STATE_ACTIVE) {
        this.pause();
      } else if (this.state === STATE_PAUSED) {
        this.resume();
      } else if (this.state === STATE_PAID_SHARE) {
        this.resume();
      }
    }

    setPauseWhenHidden(enabled) {
      this.config.pauseWhenHidden = Boolean(enabled);
      if (!this.config.pauseWhenHidden) {
        this.autoPausedByVisibility = false;
      }
    }

    startWorker() {
      if (this.worker) {
        return;
      }
      this.worker = new Worker(this.config.workerScriptUrl, { name: "joulework-worker" });
      this.worker.onmessage = (event) => {
        const message = event.data || {};
        if (message.type === "compute_error") {
          this.hasLease = false;
          this.scheduleRetry(500);
          return;
        }
        if (message.type !== "computed") {
          return;
        }
        this.lastElapsedMs = Number(message.elapsedMs || 0);
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

    connectSocket() {
      if (this.connected) {
        return;
      }
      this.manualStop = false;
      this.socket = new WebSocket(this.config.endpoint);

      this.socket.onopen = () => {
        this.connected = true;
        this.send({
          type: "hello",
          workerType: "browser",
          sessionId: this.sessionId,
          clientVersion: "0.1.0",
        });
      };

      this.socket.onmessage = (event) => {
        let payload;
        try {
          payload = JSON.parse(event.data);
        } catch (_err) {
          return;
        }
        this.handleMessage(payload);
      };

      this.socket.onclose = () => {
        this.connected = false;
        this.awaitingTask = false;
        this.hasLease = false;
        if (!this.manualStop && this.state === STATE_ACTIVE) {
          this.scheduleReconnect();
        }
      };

      this.socket.onerror = () => {
        if (this.socket) {
          this.socket.close();
        }
      };
    }

    scheduleReconnect() {
      this.clearRetry();
      this.retryTimer = window.setTimeout(() => {
        this.connectSocket();
      }, 1500);
    }

    scheduleRetry(ms) {
      this.clearRetry();
      this.retryTimer = window.setTimeout(() => {
        this.requestTask();
      }, ms);
    }

    clearRetry() {
      if (this.retryTimer !== null) {
        clearTimeout(this.retryTimer);
        this.retryTimer = null;
      }
    }

    handleMessage(payload) {
      if (!payload || typeof payload.type !== "string") {
        return;
      }

      if (payload.type === "hello_ack") {
        if (payload.sessionId) {
          this.sessionId = payload.sessionId;
        }
        if (typeof payload.targetJoules === "number" && payload.targetJoules > 0) {
          this.config.targetJoules = payload.targetJoules;
        }
        if (this.state === STATE_ACTIVE) {
          this.requestTask();
        }
        this.renderPill();
        return;
      }

      if (payload.type === "no_task") {
        this.awaitingTask = false;
        if (this.state === STATE_ACTIVE) {
          this.scheduleRetry(Number(payload.retryMs || 1000));
        }
        return;
      }

      if (payload.type === "task_assigned") {
        this.awaitingTask = false;
        this.hasLease = true;
        if (this.state !== STATE_ACTIVE) {
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
          } else {
            this.sessionJoules += (this.lastElapsedMs / 1000) * this.config.estimatedBrowserWatts;
          }

          if (!this.allowBeyondTarget && (payload.targetReached || this.sessionJoules >= this.config.targetJoules)) {
            this.state = STATE_PAID_SHARE;
            this.renderPill();
            return;
          }
        }

        this.renderPill();
        if (this.state === STATE_ACTIVE) {
          this.requestTask();
        }
        return;
      }
    }

    requestTask() {
      if (!this.connected || this.state !== STATE_ACTIVE || this.awaitingTask || this.hasLease) {
        return;
      }
      this.awaitingTask = true;
      this.send({ type: "request_task" });
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

    onBeforeUnload(event) {
      if (this.state === STATE_ACTIVE) {
        event.preventDefault();
        event.returnValue = "You're helping process work. Leave tab open to keep contributing.";
      }
    }

    destroy() {
      document.removeEventListener("visibilitychange", this.onVisibilityChange);
      window.removeEventListener("beforeunload", this.onBeforeUnload);
      this.manualStop = true;
      this.clearRetry();

      if (this.socket && this.socket.readyState <= WebSocket.OPEN) {
        this.socket.close();
      }
      if (this.worker) {
        this.worker.terminate();
      }
      if (this.bannerEl) {
        this.bannerEl.remove();
      }
      if (this.pillEl) {
        this.pillEl.remove();
      }
    }
  }

  function startJouleWork(config) {
    const widget = new JouleWorkWidget(config || {});
    widget.mount();
    return widget;
  }

  window.JouleWorkWidget = JouleWorkWidget;
  window.startJouleWork = startJouleWork;
})();
