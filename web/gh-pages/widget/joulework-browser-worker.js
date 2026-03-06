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

self.onmessage = async (event) => {
  const message = event.data;
  if (!message || message.type !== "compute") {
    return;
  }

  const { taskId, leaseId, payloadBase64 } = message;
  try {
    const bytes = decodeBase64ToBytes(payloadBase64);
    const start = performance.now();
    const hashBuffer = await crypto.subtle.digest("SHA-256", bytes);
    const elapsedMs = Math.max(1, Math.round(performance.now() - start));
    const hashHex = bytesToHex(new Uint8Array(hashBuffer));

    self.postMessage({
      type: "computed",
      taskId,
      leaseId,
      result: hashHex,
      outputHash: hashHex,
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
