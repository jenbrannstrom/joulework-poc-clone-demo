const stateEl = document.getElementById("state");
const sessionEl = document.getElementById("session");
const joulesEl = document.getElementById("joules");
const tasksEl = document.getElementById("tasks");
const unlockEl = document.getElementById("unlock");
const msgEl = document.getElementById("message");
const persistHiddenEl = document.getElementById("persist-hidden");

const startBtn = document.getElementById("start");
const pauseBtn = document.getElementById("pause");
const resumeBtn = document.getElementById("resume");
const verifyBtn = document.getElementById("verify");
const refreshBtn = document.getElementById("refresh");

function setMessage(text, isError = false) {
  msgEl.textContent = text || "";
  msgEl.classList.toggle("error", isError);
}

function withTimeout(promise, ms) {
  return Promise.race([
    promise,
    new Promise((_, reject) => {
      setTimeout(() => reject(new Error("timeout")), ms);
    }),
  ]);
}

function activeTab() {
  return new Promise((resolve, reject) => {
    chrome.tabs.query({ active: true, currentWindow: true }, (tabs) => {
      if (chrome.runtime.lastError) {
        reject(new Error(chrome.runtime.lastError.message));
        return;
      }
      if (!tabs || !tabs[0] || !tabs[0].id) {
        reject(new Error("No active tab"));
        return;
      }
      resolve(tabs[0]);
    });
  });
}

async function sendToContent(type, payload = {}) {
  const tab = await activeTab();
  return withTimeout(
    new Promise((resolve, reject) => {
      chrome.tabs.sendMessage(tab.id, { type, ...payload }, (response) => {
        if (chrome.runtime.lastError) {
          reject(new Error(chrome.runtime.lastError.message));
          return;
        }
        if (!response) {
          reject(new Error("No response from content script"));
          return;
        }
        resolve(response);
      });
    }),
    2500
  );
}

function renderStatus(status) {
  stateEl.textContent = status.state || "-";
  sessionEl.textContent = status.sessionId ? `${status.sessionId.slice(0, 6)}...` : "-";

  const localJ = Number(status.sessionJoules || 0);
  const serverJ = Number(status.serverJoules || 0);
  const targetJ = Number(status.targetJoules || 0);
  joulesEl.textContent = `${localJ.toFixed(2)}J local / ${serverJ.toFixed(2)}J server / ${targetJ.toFixed(2)}J target`;

  tasksEl.textContent = String(status.tasksCompleted || 0);
  unlockEl.textContent = status.unlocked ? "Unlocked" : "Locked";
  persistHiddenEl.checked = !Boolean(status.pauseWhenHidden);

  if (status.lastError) {
    setMessage(status.lastError, true);
  } else {
    setMessage("");
  }
}

async function refreshStatus() {
  try {
    const status = await sendToContent("jw_get_status");
    renderStatus(status);
  } catch (error) {
    setMessage(
      "Open the designated site tab first (joulework-demo.rtb.cat) so the extension can run there.",
      true
    );
  }
}

startBtn.addEventListener("click", async () => {
  try {
    const status = await sendToContent("jw_start");
    renderStatus(status);
    setMessage("Started.");
  } catch (error) {
    setMessage(error.message, true);
  }
});

pauseBtn.addEventListener("click", async () => {
  try {
    const status = await sendToContent("jw_pause");
    renderStatus(status);
    setMessage("Paused.");
  } catch (error) {
    setMessage(error.message, true);
  }
});

resumeBtn.addEventListener("click", async () => {
  try {
    const status = await sendToContent("jw_resume");
    renderStatus(status);
    setMessage("Resumed.");
  } catch (error) {
    setMessage(error.message, true);
  }
});

verifyBtn.addEventListener("click", async () => {
  try {
    const status = await sendToContent("jw_unlock_now");
    renderStatus(status);
    setMessage(status.unlocked ? "Unlocked by server verification." : "Still below target.");
  } catch (error) {
    setMessage(error.message, true);
  }
});

refreshBtn.addEventListener("click", () => {
  refreshStatus();
});

persistHiddenEl.addEventListener("change", async () => {
  try {
    const status = await sendToContent("jw_set_persist_hidden", { value: persistHiddenEl.checked });
    renderStatus(status);
    setMessage(
      persistHiddenEl.checked
        ? "Best-effort background mode enabled."
        : "Will pause automatically when tab is hidden."
    );
  } catch (error) {
    setMessage(error.message, true);
  }
});

refreshStatus();
setInterval(refreshStatus, 2000);
