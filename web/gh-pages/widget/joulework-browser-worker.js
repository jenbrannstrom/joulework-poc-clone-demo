function decodeBase64ToBytes(base64) {
  const binary = atob(base64);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i += 1) {
    bytes[i] = binary.charCodeAt(i);
  }
  return bytes;
}

function bytesToHex(bytes) {
  const hex = [];
  for (let i = 0; i < bytes.length; i += 1) {
    const value = bytes[i].toString(16);
    hex.push(value.length === 1 ? `0${value}` : value);
  }
  return hex.join("");
}

function computePiPartial(taskPayload) {
  if (
    !taskPayload ||
    typeof taskPayload.startTerm !== "number" ||
    typeof taskPayload.termCount !== "number" ||
    taskPayload.startTerm < 0 ||
    taskPayload.termCount <= 0
  ) {
    throw new Error("invalid pi task payload");
  }

  let partial = 0;
  const startTerm = Math.floor(taskPayload.startTerm);
  const termCount = Math.floor(taskPayload.termCount);
  const end = startTerm + termCount;
  for (let k = startTerm; k < end; k += 1) {
    const sign = k % 2 === 0 ? 1 : -1;
    partial += sign / (2 * k + 1);
  }

  return JSON.stringify({
    kind: "pi_leibniz_partial",
    startTerm,
    termCount,
    partialSum: partial,
  });
}

self.onmessage = async (event) => {
  const message = event.data;
  if (!message || message.type !== "compute") {
    return;
  }

  const { taskId, leaseId, payloadBase64, taskType = "sha256" } = message;
  try {
    const bytes = decodeBase64ToBytes(payloadBase64);
    const start = performance.now();
    let result;
    let outputHash = "";

    if (taskType === "pi_leibniz") {
      const payloadText = new TextDecoder().decode(bytes);
      const taskPayload = JSON.parse(payloadText);
      result = computePiPartial(taskPayload);
    } else {
      const hashBuffer = await crypto.subtle.digest("SHA-256", bytes);
      const hashHex = bytesToHex(new Uint8Array(hashBuffer));
      result = hashHex;
      outputHash = hashHex;
    }
    const elapsedMs = Math.max(1, Math.round(performance.now() - start));

    self.postMessage({
      type: "computed",
      taskId,
      leaseId,
      result,
      outputHash,
      elapsedMs,
    });
  } catch (error) {
    self.postMessage({
      type: "compute_error",
      taskId,
      leaseId,
      reason: error instanceof Error ? error.message : "compute failed",
    });
  }
};
