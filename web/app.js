const VERSION = 1;
const CHUNK_SIZE = 64 * 1024;
const LARGE_CHUNK_SIZE = 192 * 1024;
const MAX_CHUNK_SIZE = LARGE_CHUNK_SIZE;
const SIGNAL_TIMEOUT_MS = 30 * 1000;
const PAIR_TIMEOUT_MS = 5 * 60 * 1000;
const ALPHABET = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789";
const OPFS_RECEIVE_DIR = "kigo-receive-v1";
const OPFS_CHECKPOINT_BYTES = 4 * 1024 * 1024;
const OPFS_WRITE_QUEUE_HIGH_BYTES = 4 * 1024 * 1024;
const OPFS_WRITE_QUEUE_LOW_BYTES = 1 * 1024 * 1024;
const OPFS_RETENTION_MS = 7 * 24 * 60 * 60 * 1000;
const PROGRESS_RENDER_INTERVAL_MS = 100;
const CHUNK_LOG_INTERVAL_MS = 1000;
const PERFORMANCE_SAMPLE_INTERVAL_MS = 500;
const PERFORMANCE_LOG_MIN_BYTES = 1024 * 1024;
const FEATURE_DEFERRED_FILE_SHA256 = "deferred-file-sha256";
const FEATURE_BINARY_CHUNKS = "binary-chunks-v1";
const FEATURE_LARGE_CHUNKS = "large-chunks-v1";
const FEATURE_UNORDERED_DATA = "unordered-data-v1";
const FEATURE_PARALLEL_DATA = "parallel-data-v1";
const ENABLE_PARALLEL_DATA = new URLSearchParams(location.search).get("parallel") === "1";
const ENABLE_UNORDERED_DATA = false;
const BINARY_ENVELOPE_MAGIC = new Uint8Array([0x4b, 0x47, 0x45, 0x31]);
const BINARY_CHUNK_MAGIC = new Uint8Array([0x4b, 0x47, 0x43, 0x31]);
const BINARY_ENVELOPE_HEADER_BYTES = 12;
const BINARY_CHUNK_HEADER_BYTES = 21;
const UTF8_ENCODER = new TextEncoder();
const UTF8_DECODER = new TextDecoder();
const {
  base64ToBytes,
  bytesToBase64,
  parseJSONFrame,
  randomNonce,
} = globalThis.KigoSecure;
const webRTC = globalThis.KigoWebRTC.create({
  log,
  signalTimeoutMs: SIGNAL_TIMEOUT_MS,
});
globalThis.kigoWebRTC = webRTC;

const logEl = document.querySelector("#log");
const shareEl = document.querySelector("#share");
const progressEl = document.querySelector("#progress");
const cancelButton = document.querySelector("#cancelButton");
const textOutput = document.querySelector("#textOutput");
const downloads = document.querySelector("#downloads");
let busy = false;
let activeTask = null;

for (const button of document.querySelectorAll("[data-tab]")) {
  button.addEventListener("click", () => {
    document.querySelectorAll("[data-tab]").forEach((b) => b.classList.remove("active"));
    document.querySelectorAll(".panel").forEach((p) => p.classList.remove("active"));
    button.classList.add("active");
    document.querySelector(`#${button.dataset.tab}`).classList.add("active");
  });
}

const hashCode = new URLSearchParams(location.hash.slice(1)).get("c");
if (hashCode) {
  document.querySelector("#codeInput").value = normalizeCode(hashCode);
  document.querySelector('[data-tab="receive"]').click();
}

cancelButton.addEventListener("click", () => {
  if (!activeTask || activeTask.canceled) return;
  activeTask.cancel();
  log("Canceling transfer...");
});

document.querySelector("#sendFile").addEventListener("click", () => runTask(async (task) => {
  const files = Array.from(document.querySelector("#fileInput").files || []);
  if (!files.length) return log("Choose one or more files first.");
  warnLargeBrowserSend(files);
  await runSender(files.map(fileToItem), task, document.querySelector("#fileCode").value);
}));

document.querySelector("#pickFolder").addEventListener("click", () => {
  document.querySelector("#folderInput").click();
});

document.querySelector("#folderInput").addEventListener("change", () => runTask(async (task) => {
  const files = Array.from(document.querySelector("#folderInput").files || []);
  if (!files.length) return;
  warnLargeBrowserSend(files);
  await runSender(files.map(fileToItem), task, document.querySelector("#fileCode").value);
}));

function fileToItem(file) {
  return { kind: "file", file, name: safeRelativeName(file.webkitRelativePath || file.name) };
}

function warnLargeBrowserSend(files) {
  const total = files.reduce((sum, file) => sum + file.size, 0);
  if (total > 512 * 1024 * 1024) {
    log("Large browser sends may take a moment while the browser verifies file integrity.");
  }
}

document.querySelector("#sendText").addEventListener("click", () => runTask(async (task) => {
  const text = document.querySelector("#textInput").value;
  await runSender([{ kind: "text", text, name: "message.txt" }], task, document.querySelector("#textCode").value);
}));

document.querySelector("#receiveButton").addEventListener("click", () => runTask(async (task) => {
  const code = normalizeCode(document.querySelector("#codeInput").value);
  if (!validPairingCode(code)) return log("Enter a valid pairing code.");
  await runReceiver(code, task);
}));

if (hashCode) {
  queueMicrotask(() => runTask(async (task) => {
    await runReceiver(normalizeCode(hashCode), task);
  }));
}

async function runTask(fn) {
  if (busy) return;
  const task = createTask();
  activeTask = task;
  setBusy(true);
  try {
    await fn(task);
  } catch (err) {
    if (task.canceled || isCancelError(err)) {
      log("Canceled.");
    } else {
      log(`Error: ${friendlyError(err)}`);
    }
  } finally {
    task.cancel();
    setBusy(false);
    if (activeTask === task) activeTask = null;
  }
}

function setBusy(next) {
  busy = next;
  document.querySelectorAll("button, input, textarea").forEach((el) => {
    if (el.id === "folderInput") return;
    if (el.id === "cancelButton") {
      el.disabled = !next;
      return;
    }
    el.disabled = next;
  });
  if (typeof window.syncNoteControls === "function") window.syncNoteControls();
  cancelButton.classList.toggle("hidden", !next);
}

function createTask() {
  const cleanups = new Set();
  return {
    canceled: false,
    addCleanup(fn) {
      cleanups.add(fn);
      return () => cleanups.delete(fn);
    },
    cancel() {
      if (this.canceled) return;
      this.canceled = true;
      for (const cleanup of [...cleanups]) {
        try {
          cleanup();
        } catch {}
      }
      cleanups.clear();
    },
  };
}

function isCancelError(err) {
  const message = String(err?.message || err || "").toLowerCase();
  return message.includes("canceled") || message.includes("cancelled");
}

function friendlyError(err) {
  const message = String(err?.message || err || "unknown error");
  const lower = message.toLowerCase();
  if (lower.includes("room is full") || lower.includes("room is locked")) {
    return "This pairing code is already in use. Start a new transfer and use the new code.";
  }
  if (lower.includes("room expired")) {
    return "This pairing code expired. Start a new transfer to get a fresh code.";
  }
  if (lower.includes("signaling connection closed")) {
    return "Signaling connection closed. Check that the other side is still waiting and try again.";
  }
  if (lower.includes("transfer connection closed") || lower.includes("datachannel")) {
    return "Transfer connection closed. Automatic same-code reconnect was exhausted.";
  }
  if (lower.includes("timed out")) {
    return `${message}. Check the code and make sure both sides are online.`;
  }
  return message;
}

async function runSender(items, task, requestedCode = "") {
  const code = normalizeCode(requestedCode) || generateCode();
  await runTransferPeer("sender", code, task, (pipe, session) => sendTransfer(pipe, session, items, task));
}

async function runReceiver(code, task) {
  await runTransferPeer("receiver", normalizeCode(code), task, (pipe, session) => receiveTransfer(pipe, session, task));
}

async function runTransferPeer(role, code, task, transfer) {
  resetOutput();
  if (!validPairingCode(code)) throw new Error("Pairing code must be 6-64 letters/digits with optional mnemonic separators");
  const token = await roomToken(code);
  if (role === "sender") showShare(code);
  log(role === "sender" ? `Waiting for receiver with code ${code}...` : `Joining ${code}...`);
  const negotiation = await negotiateBrowserRoute(token, role, task);
  const directFirst = negotiation?.pair === "web-web";
  const unorderedData = negotiation?.features?.includes(FEATURE_UNORDERED_DATA) === true;
  const parallelData = negotiation?.features?.includes(FEATURE_PARALLEL_DATA) === true;
  if (directFirst) log("Trying direct WebRTC; TURN will start shortly if needed...");
  await webRTC.runPeerSession({
    role,
    token,
    task,
    directFirst,
    unorderedData,
    parallelData,
    onRelayStart: () => {
      log("Direct path still pending; starting TURN in parallel...");
    },
    onParallelFallback: () => {
      log("Parallel data paths unavailable; continuing on one direct path.");
    },
    onRetry: ({ nextAttempt, nextMode, maxAttempts, diagnostics }) => {
      if (nextMode === "direct") {
        log(`LAN path unavailable (${formatICEDiagnostics(diagnostics)}). Reconnecting ${nextAttempt}/${maxAttempts} with STUN...`);
      } else if (nextMode === "relay") {
        log(`Direct path unavailable (${formatICEDiagnostics(diagnostics)}). Reconnecting ${nextAttempt}/${maxAttempts} through TURN...`);
      } else {
        log(`Connection interrupted. Reconnecting ${nextAttempt}/${maxAttempts}...`);
      }
    },
  }, async ({ connectionMs, iceMode, peer, pipe, race }) => {
    log("WebRTC connected.");
    const telemetry = createTransferTelemetry(peer.pcs || [peer.pc], pipe, role, { connectionMs, race });
    const removeTelemetryCleanup = task.addCleanup(() => telemetry.stop());
    try {
      const handshakeStarted = performance.now();
      const session = await initTransferSession(
        role,
        pipe,
        code,
        peer.pc.sctp?.maxMessageSize,
        iceMode === "host" || iceMode === "all",
        unorderedData && pipe.hasDataChannel(),
      );
      telemetry.addDuration("handshakeMs", performance.now() - handshakeStarted);
      session.telemetry = telemetry;
      session.useDataChannel = pipe.hasDataChannel();
      if (session.features.includes(FEATURE_BINARY_CHUNKS)) log(`WebRTC chunk size ${session.chunkSize / 1024} KiB.`);
      if (session.features.includes(FEATURE_UNORDERED_DATA)) log("Reliable unordered file channel negotiated.");
      if (pipe.dataChannelCount() > 1) log(`Parallel WebRTC: ${pipe.dataChannelCount()} data paths.`);
      await logSelectedWebRTCPath(peer.pc);
      if (session.compression) log(`Compression ${session.compression} negotiated.`);
      await transfer(pipe, session);
      await telemetry.finish();
      for (const signalToken of peer.signalTokens || [token]) {
        webRTC.clearReconnectToken(signalToken, role);
      }
      log("Transfer complete.");
    } finally {
      removeTelemetryCleanup();
      telemetry.stop();
    }
  });
}

function formatICEDiagnostics(diagnostics) {
  const local = diagnostics?.localTypes?.join("/") || "none";
  const remote = diagnostics?.remoteTypes?.join("/") || "none";
  return `ICE ${diagnostics?.iceState || "unknown"}; local ${local}; remote ${remote}`;
}

async function logSelectedWebRTCPath(pc) {
  try {
    for (let attempt = 0; attempt < 4; attempt++) {
      const description = describeSelectedWebRTCPath(await pc.getStats());
      if (description) {
        log(`Path: ${description}.`);
        return;
      }
      if (attempt < 3) await sleep(50);
    }
  } catch {}
}

function describeSelectedWebRTCPath(stats) {
  const selected = selectedWebRTCCandidatePair(stats);
  return selected ? selected.description : "";
}

function selectedWebRTCCandidatePair(stats) {
  let pair = null;
  for (const stat of stats.values()) {
    if (stat.type === "transport" && stat.selectedCandidatePairId) {
      pair = stats.get(stat.selectedCandidatePairId) || pair;
    }
  }
  if (!pair) {
    for (const stat of stats.values()) {
      if (stat.type === "candidate-pair" && stat.state === "succeeded" && stat.nominated) {
        pair = stat;
        break;
      }
    }
  }
  if (!pair) return null;
  const local = stats.get(pair.localCandidateId);
  const remote = stats.get(pair.remoteCandidateId);
  const localType = local?.candidateType || "unknown";
  const remoteType = remote?.candidateType || "unknown";
  const protocol = String(local?.protocol || remote?.protocol || "unknown").toUpperCase();
  let description = `WebRTC (${protocol}; candidate type unavailable)`;
  if (localType === "relay" || remoteType === "relay") description = `TURN relay (${protocol})`;
  else if (localType !== "unknown" && remoteType !== "unknown") description = `direct P2P (${protocol}; ${localType}/${remoteType})`;
  return { pair, local, remote, localType, remoteType, protocol, description };
}

function createTransferTelemetry(peerConnections, pipe, role, phases = {}) {
  const pcs = (Array.isArray(peerConnections) ? peerConnections : [peerConnections])
    .filter((pc) => typeof pc?.getStats === "function");
  const createdAt = performance.now();
  const durations = {
    connectionMs: Math.max(0, phases.connectionMs || 0),
    handshakeMs: 0,
    prepareMs: 0,
    encryptMs: 0,
    decryptMs: 0,
    compressMs: 0,
    decompressMs: 0,
    sourceReadMs: 0,
    finalizeMs: 0,
  };
  let transferStartedAt = 0;
  let firstPayloadAt = 0;
  let lastPayloadAt = 0;
  let networkCompleteAt = 0;
  let payloadBytes = 0;
  let baselineStats = null;
  let networkStats = null;
  let lastStats = null;
  let maxAvailableOutgoingBitrate = 0;
  let store = null;
  let stopped = false;
  let samplePending = false;

  function snapshotTransport() {
    return pipe.metrics ? pipe.metrics() : {};
  }

  async function sample() {
    if (stopped || samplePending || pcs.length === 0) return lastStats;
    samplePending = true;
    try {
      const reports = (await Promise.all(pcs.map(async (pc) => {
        try {
          return await pc.getStats();
        } catch {
          return null;
        }
      }))).filter(Boolean);
      if (reports.length === 0) return lastStats;
      let dataBytesSent = 0;
      let dataBytesReceived = 0;
      let transportBytesSent = 0;
      let transportBytesReceived = 0;
      let availableOutgoingBitrate = 0;
      let rttMs = 0;
      const selectedPaths = [];
      for (const stats of reports) {
        const selected = selectedWebRTCCandidatePair(stats);
        if (selected) selectedPaths.push(selected);
        for (const stat of stats.values()) {
          if (stat.type !== "data-channel") continue;
          if (Number.isFinite(stat.bytesSent)) dataBytesSent += stat.bytesSent;
          if (Number.isFinite(stat.bytesReceived)) dataBytesReceived += stat.bytesReceived;
        }
        const pair = selected?.pair;
        if (Number.isFinite(pair?.bytesSent)) transportBytesSent += pair.bytesSent;
        if (Number.isFinite(pair?.bytesReceived)) transportBytesReceived += pair.bytesReceived;
        if (Number.isFinite(pair?.availableOutgoingBitrate)) {
          availableOutgoingBitrate += pair.availableOutgoingBitrate;
        }
        if (Number.isFinite(pair?.currentRoundTripTime)) {
          rttMs = Math.max(rttMs, pair.currentRoundTripTime * 1000);
        }
      }
      const primary = selectedPaths[0];
      maxAvailableOutgoingBitrate = Math.max(maxAvailableOutgoingBitrate, availableOutgoingBitrate);
      lastStats = {
        at: performance.now(),
        route: primary?.description || "",
        localType: primary?.localType || "unknown",
        remoteType: primary?.remoteType || "unknown",
        protocol: primary?.protocol || "unknown",
        pathCount: selectedPaths.length,
        rttMs,
        availableOutgoingBitrate,
        dataBytesSent,
        dataBytesReceived,
        transportBytesSent,
        transportBytesReceived,
      };
      const transport = snapshotTransport();
      lastStats.maxBufferedBytes = transport.maxBufferedBytes || 0;
      return lastStats;
    } catch {
      return lastStats;
    } finally {
      samplePending = false;
    }
  }

  const timer = setInterval(() => { sample(); }, PERFORMANCE_SAMPLE_INTERVAL_MS);

  return {
    addDuration(name, milliseconds) {
      if (Object.prototype.hasOwnProperty.call(durations, name) && Number.isFinite(milliseconds)) {
        durations[name] += Math.max(0, milliseconds);
      }
    },
    recordPayload(bytes) {
      if (!Number.isFinite(bytes) || bytes <= 0) return;
      const now = performance.now();
      if (!transferStartedAt) transferStartedAt = now;
      if (!firstPayloadAt) firstPayloadAt = now;
      lastPayloadAt = now;
      payloadBytes += bytes;
    },
    async start() {
      if (transferStartedAt) return;
      transferStartedAt = performance.now();
      baselineStats = await sample();
    },
    setStore(nextStore) {
      store = nextStore;
    },
    async markNetworkComplete() {
      if (networkCompleteAt) return;
      networkCompleteAt = performance.now();
      networkStats = await sample();
    },
    async finish() {
      if (stopped) return window.__kigoLastTransferMetrics || null;
      if (!networkCompleteAt) await this.markNetworkComplete();
      await sample();
      clearInterval(timer);
      stopped = true;
      const endStats = networkStats || lastStats || {};
      const transport = snapshotTransport();
      const appDurationMs = Math.max(0, (lastPayloadAt || networkCompleteAt || performance.now()) - (firstPayloadAt || transferStartedAt || createdAt));
      const networkDurationMs = Math.max(0, (networkCompleteAt || endStats.at || performance.now()) - (transferStartedAt || createdAt));
      const baseline = baselineStats || {};
      const networkBytesSent = Math.max(0, (endStats.dataBytesSent || endStats.transportBytesSent || 0) - (baseline.dataBytesSent || baseline.transportBytesSent || 0));
      const networkBytesReceived = Math.max(0, (endStats.dataBytesReceived || endStats.transportBytesReceived || 0) - (baseline.dataBytesReceived || baseline.transportBytesReceived || 0));
      const storeMetrics = store?.metrics ? store.metrics() : {};
      const metrics = {
        role,
        race: phases.race || null,
        payloadBytes,
        payloadDurationMs: appDurationMs,
        payloadMiBPerSecond: appDurationMs > 0 ? payloadBytes / 1024 / 1024 / (appDurationMs / 1000) : 0,
        networkDurationMs,
        rtcBytes: role === "sender" ? networkBytesSent : networkBytesReceived,
        rtcMiBPerSecond: networkDurationMs > 0
          ? (role === "sender" ? networkBytesSent : networkBytesReceived) / 1024 / 1024 / (networkDurationMs / 1000)
          : 0,
        route: endStats.route || baseline.route || "",
        localCandidateType: endStats.localType || baseline.localType || "unknown",
        remoteCandidateType: endStats.remoteType || baseline.remoteType || "unknown",
        protocol: endStats.protocol || baseline.protocol || "unknown",
        pathCount: endStats.pathCount || baseline.pathCount || 0,
        rttMs: endStats.rttMs || baseline.rttMs || 0,
        maxAvailableOutgoingBitrate: maxAvailableOutgoingBitrate || endStats.availableOutgoingBitrate || baseline.availableOutgoingBitrate || 0,
        maxBufferedBytes: Math.max(transport.maxBufferedBytes || 0, endStats.maxBufferedBytes || 0),
        sendWaitMs: transport.totalWaitMs || 0,
        sendWaitCount: transport.waitCount || 0,
        sentFrameBytes: transport.sentBytes || 0,
        receivedFrameBytes: transport.receivedBytes || 0,
        sentDataFrameBytes: transport.dataSentBytes || 0,
        receivedDataFrameBytes: transport.dataReceivedBytes || 0,
        durations: { ...durations },
        storage: storeMetrics,
      };
      window.__kigoLastTransferMetrics = metrics;
      logTransferPerformance(metrics);
      return metrics;
    },
    stop() {
      if (stopped) return;
      stopped = true;
      clearInterval(timer);
    },
  };
}

function logTransferPerformance(metrics) {
  if (!metrics || metrics.payloadBytes < PERFORMANCE_LOG_MIN_BYTES) return;
  const appRate = formatBytes(metrics.payloadMiBPerSecond * 1024 * 1024);
  const rtcRate = formatBytes(metrics.rtcMiBPerSecond * 1024 * 1024);
  const waitRatio = metrics.networkDurationMs > 0 ? Math.round(metrics.sendWaitMs / metrics.networkDurationMs * 100) : 0;
  log(`Performance: payload ${formatBytes(metrics.payloadBytes)} in ${formatDuration(metrics.payloadDurationMs)} (${appRate}/s); RTC ${rtcRate}/s.`);
  log(`WebRTC stats: ${metrics.route || "path unavailable"}, RTT ${formatDuration(metrics.rttMs)}, max available ${metrics.maxAvailableOutgoingBitrate > 0 ? `${formatBytes(metrics.maxAvailableOutgoingBitrate / 8)}/s` : "unavailable"}.`);
  log(`DataChannel: send wait ${formatDuration(metrics.sendWaitMs)} (${waitRatio}% of network time), peak buffer ${formatBytes(metrics.maxBufferedBytes)}.`);
  const d = metrics.durations;
  log(`CPU stages: AES ${formatDuration(d.encryptMs + d.decryptMs)}, compression ${formatDuration(d.compressMs + d.decompressMs)}, file read ${formatDuration(d.sourceReadMs)}, finalize ${formatDuration(d.finalizeMs)}.`);
  if (metrics.storage?.type === "opfs") {
    log(`OPFS (${metrics.storage.mode || "async"}): writes ${formatDuration(metrics.storage.writeMs)}, queue wait ${formatDuration(metrics.storage.queueWaitMs)}, peak queue ${formatBytes(metrics.storage.maxQueuedBytes)}.`);
  }
}

function formatDuration(milliseconds) {
  const value = Math.max(0, milliseconds || 0);
  if (value < 1000) return `${Math.round(value)} ms`;
  return `${(value / 1000).toFixed(value >= 10000 ? 0 : 1)} s`;
}

async function initTransferSession(role, pipe, code, maxMessageSize, allowLargeChunks = true, allowUnorderedData = false) {
  const localFeatures = supportedTransferFeatures(maxMessageSize, allowLargeChunks, allowUnorderedData);
  if (role === "sender") {
    const senderNonce = randomNonce();
    const compressions = supportedCompressions();
    await pipe.send({ type: "hello", version: VERSION, sender_nonce: senderNonce, compressions, features: localFeatures });
    const ack = parseJSONFrame(await pipe.recv());
    validateHello(ack, "hello_ack");
    if (ack.compression && !compressions.includes(ack.compression)) {
      throw new Error(`receiver selected unsupported compression ${ack.compression}`);
    }
    const features = Array.isArray(ack.features) ? ack.features : [];
    if (features.some((feature) => !localFeatures.includes(feature))) {
      throw new Error("receiver selected unsupported transfer feature");
    }
    return deriveSession(code, senderNonce, ack.receiver_nonce, role, ack.compression || "", features);
  }
  const hello = parseJSONFrame(await pipe.recv());
  validateHello(hello, "hello");
  const receiverNonce = randomNonce();
  const compression = selectCompression(hello.compressions || []);
    const features = selectTransferFeatures(hello.features, maxMessageSize, allowLargeChunks, allowUnorderedData);
  await pipe.send({ type: "hello_ack", version: VERSION, receiver_nonce: receiverNonce, compression, features });
  return deriveSession(code, hello.sender_nonce, receiverNonce, role, compression, features);
}

function supportedTransferFeatures(maxMessageSize, allowLargeChunks = true, allowUnorderedData = false) {
  return [FEATURE_DEFERRED_FILE_SHA256, FEATURE_BINARY_CHUNKS]
    .concat(allowLargeChunks && canUseLargeChunks(maxMessageSize) ? [FEATURE_LARGE_CHUNKS] : [])
    .concat(allowUnorderedData ? [FEATURE_UNORDERED_DATA] : []);
}

function selectTransferFeatures(offered, maxMessageSize, allowLargeChunks = true, allowUnorderedData = false) {
  const accepted = Array.isArray(offered) ? offered : [];
  return supportedTransferFeatures(maxMessageSize, allowLargeChunks, allowUnorderedData).filter((feature) => accepted.includes(feature)
    && (![FEATURE_LARGE_CHUNKS, FEATURE_UNORDERED_DATA].includes(feature) || accepted.includes(FEATURE_BINARY_CHUNKS)));
}

function canUseLargeChunks(maxMessageSize) {
  if (!Number.isFinite(maxMessageSize) || maxMessageSize <= 0) return true;
  return maxMessageSize >= LARGE_CHUNK_SIZE + BINARY_CHUNK_HEADER_BYTES + BINARY_ENVELOPE_HEADER_BYTES + 16;
}

function validateHello(msg, wantType) {
  if (!msg || msg.type !== wantType) {
    throw new Error(`invalid hello type: got ${msg?.type} want ${wantType}`);
  }
  if (msg.version !== VERSION) {
    throw new Error(`unsupported hello version ${msg.version}`);
  }
  if (wantType === "hello" && !msg.sender_nonce) {
    throw new Error("invalid sender hello nonce");
  }
  if (wantType === "hello_ack" && !msg.receiver_nonce) {
    throw new Error("invalid receiver hello ack nonce");
  }
  if (msg.compression && msg.compression !== "gzip") {
    throw new Error(`unsupported hello compression ${msg.compression}`);
  }
}

function supportedCompressions() {
  if (typeof CompressionStream !== "function" || typeof DecompressionStream !== "function") return [];
  try {
    new CompressionStream("gzip");
    new DecompressionStream("gzip");
    return ["gzip"];
  } catch {
    return [];
  }
}

function selectCompression(offered) {
  return Array.isArray(offered) && offered.includes("gzip") && supportedCompressions().includes("gzip") ? "gzip" : "";
}

async function sendTransfer(pipe, session, items, task) {
  let seq = 0;
  const chunkSize = session.chunkSize || CHUNK_SIZE;
  const manifestItems = [];
  const prepareStarted = performance.now();
  const prepareProgress = createProgress("Preparing", totalItemBytes(items));
  let preparedBytes = 0;
  const deferredFileSHA256 = session.features.includes(FEATURE_DEFERRED_FILE_SHA256);
  for (const item of items) {
    throwIfCanceled(task);
    if (item.kind === "file") {
      const sampleSHA256 = await fileSampleSHA256(item.file);
      const sha256 = deferredFileSHA256 ? "" : await fileSHA256(item.file, (done) => {
        throwIfCanceled(task);
        prepareProgress.update(preparedBytes + done, item.name || item.file.name);
      });
      manifestItems.push({
        kind: "file",
        name: item.name || safeRelativeName(item.file.name),
        size: item.file.size,
        mtime: item.file.lastModified,
        mode: 0,
        sha256: sha256 || undefined,
        sample_sha256: sampleSHA256,
        chunk_size: chunkSize,
        resume_supported: true,
      });
      preparedBytes += item.file.size;
      prepareProgress.update(preparedBytes, item.name || item.file.name);
    } else {
      const textBytes = UTF8_ENCODER.encode(item.text);
      manifestItems.push({
        kind: "text",
        name: item.name || "message.txt",
        size: textBytes.length,
        sha256: await bytesSHA256(textBytes),
        chunk_size: chunkSize,
        resume_supported: false,
      });
      preparedBytes += textBytes.length;
      prepareProgress.update(preparedBytes, item.name || "message.txt");
    }
  }
  session.telemetry?.addDuration("prepareMs", performance.now() - prepareStarted);
  const manifest = {
    version: VERSION,
    items: manifestItems,
    streams: manifestItems.map((_, item) => ({ id: item + 1, item })),
  };
  const streamPlan = createMuxPlan(manifest);
  await sendEncrypted(pipe, session, seq++, { type: "manifest", version: VERSION, manifest });
  const resumeResult = await negotiateSenderResume(pipe, session, manifest, items, streamPlan, seq);
  const resumeOffsets = resumeResult.offsets;
  seq = resumeResult.nextSeq;
  await session.telemetry?.start();
  const sendProgress = createStreamProgress("Sending", manifest, resumeOffsets);
  sendProgress.show("resume baseline");

  for (let i = 0; i < items.length; i++) {
    throwIfCanceled(task);
    const item = items[i];
    if (item.kind !== "text") continue;
    const data = UTF8_ENCODER.encode(item.text);
    const encoded = await encodeTransferChunk(data, session, {});
    const streamID = streamPlan.streamForItem(i);
    await sendEncrypted(pipe, session, seq++, { type: "stream_open", item: i, stream: streamID, at: Date.now() });
    await sendEncryptedChunk(pipe, session, seq++, {
      type: "chunk",
      item: i,
      stream: streamID,
      offset: 0,
      encoding: encoded.encoding || undefined,
    }, encoded.data);
    session.telemetry?.recordPayload(data.length);
    sendProgress.addStream(streamID, data.length);
    await sendEncrypted(pipe, session, seq++, { type: "stream_end", item: i, stream: streamID, at: Date.now() });
  }

  const fileStates = [];
  for (let i = 0; i < items.length; i++) {
    throwIfCanceled(task);
    const item = items[i];
    if (item.kind !== "file") continue;
    const offset = clampNumber(resumeOffsets.get(i) || 0, 0, item.file.size);
    fileStates.push({
      itemIndex: i,
      streamID: streamPlan.streamForItem(i),
      item,
      offset,
      done: offset >= item.file.size,
      ended: false,
      compressionState: {},
    });
  }
  for (const state of fileStates) {
    await sendEncrypted(pipe, session, seq++, { type: "stream_open", item: state.itemIndex, stream: state.streamID, at: Date.now() });
    if (state.done) {
      state.ended = true;
      await sendEncrypted(pipe, session, seq++, { type: "stream_end", item: state.itemIndex, stream: state.streamID, at: Date.now() });
    }
  }
  const stateByStream = new Map(fileStates.map((state) => [state.streamID, state]));
  const scheduler = createWeightedScheduler(
    fileStates
      .filter((state) => !state.done)
      .map((state) => ({ id: state.streamID, weight: fileStreamWeight(state.item.file.size - state.offset) })),
    chunkSize,
  );
  const sendChunkLog = createChunkProgressLogger("sent");
  for (;;) {
    throwIfCanceled(task);
    const maxChunk = chunkSize;
    const turn = scheduler.next(maxChunk);
    if (!turn) break;
    const state = stateByStream.get(turn.streamID);
    if (!state) throw new Error(`scheduler returned unknown stream ${turn.streamID}`);
    const readStarted = performance.now();
    const chunk = new Uint8Array(await state.item.file.slice(state.offset, state.offset + turn.budget).arrayBuffer());
    session.telemetry?.addDuration("sourceReadMs", performance.now() - readStarted);
    const encoded = await encodeTransferChunk(chunk, session, state.compressionState);
    await sendEncryptedChunk(pipe, session, seq++, {
      type: "chunk",
      item: state.itemIndex,
      stream: state.streamID,
      offset: state.offset,
      encoding: encoded.encoding || undefined,
    }, encoded.data);
    session.telemetry?.recordPayload(chunk.length);
    state.offset += chunk.length;
    state.done = state.offset >= state.item.file.size;
    sendProgress.addStream(state.streamID, chunk.length);
    sendChunkLog({
      itemName: state.item.name,
      offset: state.offset,
      bytes: chunk.length,
      total: state.item.file.size,
      complete: state.done,
    });
    if (state.done && !state.ended) {
      state.ended = true;
      await sendEncrypted(pipe, session, seq++, { type: "stream_end", item: state.itemIndex, stream: state.streamID, at: Date.now() });
    }
    scheduler.commit(state.streamID, chunk.length, state.done);
  }
  logCompressionStats(session);
  await sendEncrypted(pipe, session, seq++, { type: "done", at: Date.now() });
  await session.telemetry?.markNetworkComplete();
  const complete = await recvEncrypted(pipe, session);
  if (complete.type !== "complete") throw new Error("receiver did not confirm completion");
}

async function negotiateSenderResume(pipe, session, manifest, sourceItems, streamPlan = createMuxPlan(manifest), sendSeq = 0) {
  const resumable = manifest.items.some((item) => item.kind === "file" && item.resume_supported);
  const offsets = new Map();
  if (!resumable) return { offsets, nextSeq: sendSeq };
  const msg = await recvEncrypted(pipe, session);
  if (msg.type !== "resume") throw new Error(`expected resume, got ${msg.type}`);
  const accepted = [];
  for (const entry of msg.resume || []) {
    let offset = validateResumeEntry(manifest, entry, streamPlan);
    const source = sourceItems[entry.item];
    if (!source || source.kind !== "file") throw new Error(`resume requested for non-file item ${entry.item}`);
    if (entry.skip) {
      if (offset !== source.file.size) throw new Error(`skip offset for item ${entry.item} must equal file size`);
      if (entry.complete) log(`Skipped already-complete ${manifest.items[entry.item].name}.`);
    } else {
      let knownFullHash = manifest.items[entry.item].sha256 || "";
      if (!knownFullHash) {
        knownFullHash = await fileSHA256(source.file);
      }
      if (offset > 0 && entry.prefix_sha256) {
        const sourcePrefix = offset === source.file.size && knownFullHash
          ? knownFullHash.toLowerCase()
          : await filePrefixSHA256(source.file, offset);
        if (sourcePrefix !== entry.prefix_sha256.toLowerCase()) {
          log(`Resume prefix mismatch for ${manifest.items[entry.item].name}; restarting from 0.`);
          offset = 0;
        }
      }
      manifest.items[entry.item].sha256 = knownFullHash.toLowerCase();
    }
    offsets.set(entry.item, offset);
    accepted.push({
      item: entry.item,
      stream: entry.stream,
      offset,
      sha256: entry.skip ? undefined : manifest.items[entry.item].sha256,
      skip: Boolean(entry.skip),
      complete: Boolean(entry.complete),
    });
  }
  await sendEncrypted(pipe, session, sendSeq++, { type: "resume_accept", resume: accepted, at: Date.now() });
  return { offsets, nextSeq: sendSeq };
}

async function receiveTransfer(pipe, session, task) {
  let manifest = null;
  let fileStore = null;
  const textParts = new Map();
  const textOffsets = new Map();
  let streamPlan = null;
  let streamTracker = null;
  let sendSeq = 0;
  let receiveProgress = null;
  const receiveChunkLog = createChunkProgressLogger("received");
  try {
    for (;;) {
      throwIfCanceled(task);
      const msg = await recvEncrypted(pipe, session);
      if (msg.type === "manifest") {
        manifest = msg.manifest;
        streamPlan = validateManifest(manifest);
        streamTracker = createStreamTracker(streamPlan);
        fileStore = await createBrowserFileStore(manifest, streamPlan);
        session.telemetry?.setStore(fileStore);
        log(`Receiving ${manifest.items.length} item(s).`);
        receiveProgress = createStreamProgress("Receiving", manifest, fileStore.offsets());
        receiveProgress.show("resume baseline");
        manifest.items.forEach((item, i) => {
          if (item.kind === "text") {
            textParts.set(i, []);
            textOffsets.set(i, 0);
          }
        });
        const resume = fileStore.resumeEntries();
        if (resume.length) {
          await sendEncrypted(pipe, session, sendSeq++, { type: "resume", resume, at: Date.now() });
          const accepted = await recvEncrypted(pipe, session);
          if (accepted.type !== "resume_accept") throw new Error(`expected resume_accept, got ${accepted.type}`);
          const acceptedItems = new Set();
          for (const entry of accepted.resume || []) {
            validateResumeEntry(manifest, entry, streamPlan);
            if (entry.skip) throw new Error(`sender unexpectedly skipped browser item ${entry.item}`);
            applyAcceptedFileSHA256(manifest, entry, session);
            if (acceptedItems.has(entry.item)) throw new Error(`duplicate resume_accept entry for item ${entry.item}`);
            acceptedItems.add(entry.item);
          }
          if (acceptedItems.size !== resume.length) throw new Error("resume_accept did not cover every resumable file");
          await fileStore.applyResumeAccept(accepted.resume || []);
          receiveProgress = createStreamProgress("Receiving", manifest, fileStore.offsets());
          receiveProgress.show("accepted resume");
        }
        await session.telemetry?.start();
      } else if (msg.type === "stream_open") {
        if (!manifest) throw new Error("stream_open arrived before manifest");
        streamTracker.acceptOpen(manifest, msg);
      } else if (msg.type === "stream_end") {
        if (!manifest) throw new Error("stream_end arrived before manifest");
        streamTracker.acceptEnd(manifest, msg);
      } else if (msg.type === "chunk") {
        if (!manifest) throw new Error("chunk arrived before manifest");
        const binding = streamTracker.acceptChunk(manifest, msg);
        const item = manifest.items[msg.item];
        const encoded = msg.data instanceof Uint8Array ? msg.data : base64ToBytes(msg.data);
        const data = await decodeTransferChunk(encoded, msg.encoding || "", session);
        const dataLength = data.length;
        if (item.kind === "file") {
          await fileStore.writeChunk(msg.item, msg.offset, data);
        }
        if (item.kind === "text") {
          validateChunkBounds(item, msg.offset, dataLength, textOffsets.get(msg.item) || 0);
          textParts.get(msg.item).push(data);
          textOffsets.set(msg.item, (textOffsets.get(msg.item) || 0) + dataLength);
        }
        if (item.kind !== "file" && item.kind !== "text") {
          throw new Error(`received chunk for metadata item ${item.name}`);
        }
        session.telemetry?.recordPayload(dataLength);
        if (receiveProgress) receiveProgress.addStream(binding.id, dataLength);
        const receivedBytes = msg.offset + dataLength;
        receiveChunkLog({
          itemName: item.name,
          offset: msg.offset,
          bytes: dataLength,
          total: item.size,
          complete: receivedBytes >= item.size,
        });
      } else if (msg.type === "done") {
        await session.telemetry?.markNetworkComplete();
        const finalizeStarted = performance.now();
        const completedFiles = await fileStore.finalize();
        session.telemetry?.addDuration("finalizeMs", performance.now() - finalizeStarted);
        const zipEntries = [];
        const fileLinks = [];
        for (const [i, completed] of completedFiles) {
          const item = manifest.items[i];
          zipEntries.push({
            kind: "file",
            name: safeRelativeName(item.name || "kigo-file"),
            blob: completed.blob,
            parts: completed.parts,
            mode: item.mode,
            mtime: item.mtime,
          });
          const a = document.createElement("a");
          a.href = URL.createObjectURL(completed.blob);
          a.download = downloadName(item.name || "kigo-file");
          a.textContent = `Download ${item.name || a.download}`;
          downloads.append(a);
          fileLinks.push(a);
        }
        for (const item of manifest.items) {
          if (item.kind === "directory") {
            zipEntries.push({ kind: "directory", name: safeRelativeName(item.name), parts: [], mode: item.mode, mtime: item.mtime });
          } else if (item.kind === "symlink") {
            zipEntries.push({
              kind: "symlink",
              name: safeRelativeName(item.name),
              parts: [UTF8_ENCODER.encode(item.target)],
              mode: item.mode,
              mtime: item.mtime,
            });
          }
        }
        const needsZip = zipEntries.some((entry) => entry.kind !== "file") || zipEntries.length > 1 || zipEntries.some((entry) => entry.name.includes("/"));
        let automaticDownload = null;
        if (needsZip) {
          for (const entry of zipEntries) {
            if (entry.kind === "file" && !entry.parts) {
              entry.parts = [new Uint8Array(await entry.blob.arrayBuffer())];
            }
          }
          const zip = makeZip(zipEntries);
          const a = document.createElement("a");
          a.href = URL.createObjectURL(new Blob(zip, { type: "application/zip" }));
          a.download = "kigo-transfer.zip";
          a.textContent = "Download all as ZIP";
          downloads.prepend(a);
          automaticDownload = a;
        } else if (fileLinks.length === 1) {
          automaticDownload = fileLinks[0];
        }
        for (const [i, parts] of textParts) {
          const data = concat(parts);
          await verifyItemBytes(manifest.items[i], data);
          const text = UTF8_DECODER.decode(data);
          textOutput.textContent = text;
          textOutput.classList.remove("hidden");
        }
        if (automaticDownload) {
          automaticDownload.click();
          log("Download started automatically. Use the link above if your browser blocked it.");
        }
        await sendEncrypted(pipe, session, sendSeq++, { type: "complete", at: Date.now() });
        await fileStore.cleanup();
        return;
      }
    }
  } finally {
    if (fileStore) {
      try {
        await fileStore.close();
      } catch {}
    }
  }
}

async function createBrowserFileStore(manifest, streamPlan) {
  const canPersist = navigator.storage?.getDirectory &&
    manifest.items.every((item) => item.kind !== "file" || !item.resume_supported ||
      /^[0-9a-fA-F]{64}$/.test(item.sha256 || item.sample_sha256 || ""));
  if (canPersist) {
    try {
      return await createOPFSFileStore(manifest, streamPlan);
    } catch (err) {
      log(`Persistent receive storage unavailable; using memory (${err.message}).`);
    }
  }
  return createMemoryFileStore(manifest, streamPlan);
}

function createBrowserResumeState(manifest, streamPlan, states, { includePrefix = false, reset }) {
  return {
    offsets() {
      return new Map([...states].map(([itemID, state]) => [itemID, state.offset]));
    },
    resumeEntries() {
      return [...states]
        .filter(([, state]) => state.item.resume_supported)
        .map(([itemID, state]) => ({
          item: itemID,
          stream: streamPlan.streamForItem(itemID),
          offset: state.offset,
          prefix_sha256: includePrefix && state.offset > 0 ? state.prefixSHA256 : undefined,
        }));
    },
    async applyResumeAccept(entries) {
      const accepted = indexResumeEntries(manifest, streamPlan, entries);
      for (const [itemID, state] of states) {
        if (!state.item.resume_supported) continue;
        const entry = accepted.get(itemID);
        if (!entry) throw new Error(`resume_accept missing file item ${itemID}`);
        if (entry.skip) throw new Error(`sender unexpectedly skipped browser item ${itemID}`);
        if (entry.offset > state.offset) throw new Error(`sender increased browser resume offset for item ${itemID}: ${entry.offset}`);
        if (entry.offset !== state.offset) await reset(state, entry.offset);
      }
    },
  };
}

function createMemoryFileStore(manifest, streamPlan) {
  const states = new Map();
  manifest.items.forEach((item, itemID) => {
    if (item.kind === "file") states.set(itemID, { item, parts: [], offset: 0 });
  });
  const resume = createBrowserResumeState(manifest, streamPlan, states, {
    reset: (state, offset) => {
      state.parts = [];
      state.offset = offset;
    },
  });
  return {
    persistent: false,
    ...resume,
    async writeChunk(itemID, offset, data) {
      const state = states.get(itemID);
      if (!state) throw new Error(`file output is not open for item ${itemID}`);
      validateChunkBounds(state.item, offset, data.length, state.offset);
      state.parts.push(data);
      state.offset += data.length;
    },
    async finalize() {
      const completed = new Map();
      for (const [itemID, state] of states) {
        await verifyItemParts(state.item, state.parts);
        completed.set(itemID, { blob: new Blob(state.parts), parts: state.parts });
      }
      return completed;
    },
    metrics() {
      return { type: "memory", maxQueuedBytes: 0 };
    },
    async close() {},
    async cleanup() {},
  };
}

async function createOPFSSyncWorker(states) {
  if (typeof Worker !== "function") {
    return null;
  }
  let worker;
  let nextID = 1;
  let closed = false;
  const pending = new Map();
  const rejectPending = (err) => {
    for (const entry of pending.values()) entry.reject(err);
    pending.clear();
  };
  try {
    worker = new Worker("/opfs-worker.js");
    worker.onmessage = (event) => {
      const message = event.data || {};
      const entry = pending.get(message.id);
      if (!entry) return;
      pending.delete(message.id);
      if (message.type === "error") entry.reject(new Error(message.error || "OPFS worker failed"));
      else entry.resolve(message);
    };
    worker.onerror = (event) => {
      const message = event?.message || "OPFS worker failed";
      rejectPending(new Error(message));
    };
    const request = (message, transfer = []) => {
      if (closed) return Promise.reject(new Error("OPFS worker is closed"));
      const id = nextID++;
      return new Promise((resolve, reject) => {
        pending.set(id, { resolve, reject });
        try {
          worker.postMessage({ id, ...message }, transfer);
        } catch (err) {
          pending.delete(id);
          reject(err);
        }
      });
    };
    await request({
      type: "init",
      entries: [...states].map(([itemID, state]) => ({ itemID, handle: state.handle })),
    });
    return {
      mode: "sync-worker",
      async write(itemID, offset, data) {
        const transferable = data.byteOffset === 0 && data.byteLength === data.buffer.byteLength
          ? data.buffer
          : data.slice().buffer;
        await request({ type: "write", itemID, offset, data: transferable }, [transferable]);
      },
      flush(itemID) {
        return request({ type: "flush", itemID });
      },
      truncate(itemID, size) {
        return request({ type: "truncate", itemID, size });
      },
      async close() {
        if (closed) return;
        try {
          await request({ type: "close" });
        } finally {
          closed = true;
          rejectPending(new Error("OPFS worker closed"));
          worker.terminate();
        }
      },
    };
  } catch {
    if (worker) {
      rejectPending(new Error("OPFS sync worker unavailable"));
      worker.terminate();
    }
    return null;
  }
}

async function createOPFSFileStore(manifest, streamPlan) {
  const root = await navigator.storage.getDirectory();
  const directory = await root.getDirectoryHandle(OPFS_RECEIVE_DIR, { create: true });
  await pruneOPFSReceiveDirectory(directory, Date.now());
  const states = new Map();
  for (let itemID = 0; itemID < manifest.items.length; itemID++) {
    const item = manifest.items[itemID];
    if (item.kind !== "file") continue;
    const key = await browserPartialKey(item);
    const fileName = `${key}.part`;
    const handle = await directory.getFileHandle(fileName, { create: true });
    let file = await handle.getFile();
    let offset = item.resume_supported ? Math.min(file.size, item.size) : 0;
    const needsTruncate = file.size !== offset;
    let prefixSHA256 = "";
    if (offset > 0) {
      prefixSHA256 = await filePrefixSHA256(file, offset);
      log(`Found saved partial for ${item.name}: ${offset}/${item.size} bytes.`);
    }
    states.set(itemID, {
      item,
      itemID,
      handle,
      fileName,
      offset,
      needsTruncate,
      persistedOffset: offset,
      committedOffset: offset,
      prefixSHA256,
      writer: null,
      queue: [],
      queuedBytes: 0,
      flushing: null,
      flushError: null,
      drainWaiters: [],
      writeMs: 0,
      queueWaitMs: 0,
      maxQueuedBytes: 0,
      checkpointMs: 0,
    });
  }

  const syncWorker = await createOPFSSyncWorker(states);
  if (!syncWorker && [...states.values()].some((state) => typeof state.handle.createWritable !== "function")) {
    throw new Error("OPFS writable file handles are unavailable");
  }
  for (const state of states.values()) {
    if (!state.needsTruncate) continue;
    if (syncWorker) await syncWorker.truncate(state.itemID, state.offset);
    else await truncateOPFSFile(state.handle, state.offset);
    state.needsTruncate = false;
  }

  async function checkpoint(state) {
    const started = performance.now();
    if (syncWorker) {
      await syncWorker.flush(state.itemID);
      state.committedOffset = state.persistedOffset;
      state.checkpointMs += performance.now() - started;
      return;
    }
    if (!state.writer) return;
    const writer = state.writer;
    state.writer = null;
    await writer.close();
    state.committedOffset = state.persistedOffset;
    state.checkpointMs += performance.now() - started;
  }

  function notifyQueueLow(state) {
    if (state.queuedBytes > OPFS_WRITE_QUEUE_LOW_BYTES) return;
    const waiters = state.drainWaiters.splice(0);
    for (const waiter of waiters) waiter.resolve();
  }

  function failQueueWaiters(state, err) {
    const waiters = state.drainWaiters.splice(0);
    for (const waiter of waiters) waiter.reject(err);
  }

  async function flushState(state) {
    if (state.flushError) throw state.flushError;
    if (state.flushing) return state.flushing;
    state.flushing = (async () => {
      try {
        while (state.queue.length) {
          if (!syncWorker && !state.writer) {
            state.writer = await state.handle.createWritable({ keepExistingData: true });
          }
          const entry = state.queue.shift();
          state.queuedBytes -= entry.data.length;
          const entryBytes = entry.data.length;
          const writeStarted = performance.now();
          if (syncWorker) await syncWorker.write(state.itemID, entry.offset, entry.data);
          else await state.writer.write({ type: "write", position: state.persistedOffset, data: entry.data });
          state.writeMs += performance.now() - writeStarted;
          state.persistedOffset += entryBytes;
          if (state.persistedOffset === state.item.size || state.persistedOffset - state.committedOffset >= OPFS_CHECKPOINT_BYTES) {
            await checkpoint(state);
          }
          notifyQueueLow(state);
        }
      } catch (err) {
        state.flushError = err;
        failQueueWaiters(state, err);
        throw err;
      } finally {
        state.flushing = null;
      }
    })();
    state.flushing.catch(() => {});
    return state.flushing;
  }

  async function drainState(state) {
    while (state.queue.length || state.flushing) {
      if (state.flushError) throw state.flushError;
      const flushing = state.flushing || flushState(state);
      await flushing;
    }
    if (state.flushError) throw state.flushError;
  }

  async function waitForQueueLow(state) {
    if (state.flushError) throw state.flushError;
    if (state.queuedBytes <= OPFS_WRITE_QUEUE_LOW_BYTES) return;
    await new Promise((resolve, reject) => state.drainWaiters.push({ resolve, reject }));
  }

  async function truncateState(state, offset) {
    await drainState(state);
    if (syncWorker) {
      await syncWorker.truncate(state.itemID, offset);
    } else {
      if (state.writer) {
        try {
          await state.writer.abort();
        } catch {}
        state.writer = null;
      }
      await truncateOPFSFile(state.handle, offset);
    }
    state.offset = offset;
    state.persistedOffset = offset;
    state.committedOffset = offset;
    state.prefixSHA256 = "";
  }

  async function closeStore() {
    for (const state of states.values()) {
      await drainState(state);
      await checkpoint(state);
    }
    if (syncWorker) await syncWorker.close();
  }

  const resume = createBrowserResumeState(manifest, streamPlan, states, {
    includePrefix: true,
    reset: async (state, offset) => {
      await truncateState(state, offset);
      log(`Sender accepted ${state.item.name} resume at ${offset} bytes.`);
    },
  });

  return {
    persistent: true,
    ...resume,
    async writeChunk(itemID, offset, data) {
      const state = states.get(itemID);
      if (!state) throw new Error(`file output is not open for item ${itemID}`);
      validateChunkBounds(state.item, offset, data.length, state.offset);
      if (state.flushError) throw state.flushError;
      state.queue.push({ offset, data });
      state.queuedBytes += data.length;
      state.maxQueuedBytes = Math.max(state.maxQueuedBytes, state.queuedBytes);
      state.offset += data.length;
      flushState(state);
      if (state.queuedBytes > OPFS_WRITE_QUEUE_HIGH_BYTES) {
        const waitStarted = performance.now();
        await waitForQueueLow(state);
        state.queueWaitMs += performance.now() - waitStarted;
      }
    },
    async finalize() {
      const completed = new Map();
      for (const [itemID, state] of states) {
        await drainState(state);
        await checkpoint(state);
        const file = await state.handle.getFile();
        await verifyItemFile(state.item, file);
        completed.set(itemID, { blob: file, parts: null });
      }
      return completed;
    },
    metrics() {
      let writeMs = 0;
      let queueWaitMs = 0;
      let maxQueuedBytes = 0;
      let checkpointMs = 0;
      for (const state of states.values()) {
        writeMs += state.writeMs;
        queueWaitMs += state.queueWaitMs;
        maxQueuedBytes = Math.max(maxQueuedBytes, state.maxQueuedBytes, state.queuedBytes);
        checkpointMs += state.checkpointMs;
      }
      return { type: "opfs", mode: syncWorker?.mode || "async", writeMs, queueWaitMs, maxQueuedBytes, checkpointMs };
    },
    close: closeStore,
    cleanup: closeStore,
  };
}

function indexResumeEntries(manifest, streamPlan, entries) {
  const accepted = new Map();
  for (const entry of entries) {
    entry.offset = validateResumeEntry(manifest, entry, streamPlan);
    if (accepted.has(entry.item)) throw new Error(`duplicate resume_accept entry for item ${entry.item}`);
    accepted.set(entry.item, entry);
  }
  return accepted;
}

async function browserPartialKey(item) {
  const identity = JSON.stringify([
    VERSION,
    safeRelativeName(item.name),
    item.size,
    String(item.sample_sha256 || item.sha256 || "").toLowerCase(),
  ]);
  return bytesSHA256(UTF8_ENCODER.encode(identity));
}

async function truncateOPFSFile(handle, size) {
  const writer = await handle.createWritable({ keepExistingData: true });
  try {
    await writer.truncate(size);
    await writer.close();
  } catch (err) {
    try {
      await writer.abort();
    } catch {}
    throw err;
  }
}

async function pruneOPFSReceiveDirectory(directory, now) {
  for await (const [name, handle] of directory.entries()) {
    if (handle.kind !== "file" || !name.endsWith(".part")) continue;
    try {
      const file = await handle.getFile();
      if (now - file.lastModified > OPFS_RETENTION_MS) {
        await directory.removeEntry(name);
      }
    } catch {}
  }
}

function throwIfCanceled(task) {
  if (task?.canceled) throw new Error("transfer canceled");
}

async function deriveSession(code, senderNonce, receiverNonce, role, compression = "", features = []) {
  const sendInfo = role === "sender" ? "kigo-v1 sender-to-receiver aes-128-gcm" : "kigo-v1 receiver-to-sender aes-128-gcm";
  const recvInfo = role === "sender" ? "kigo-v1 receiver-to-sender aes-128-gcm" : "kigo-v1 sender-to-receiver aes-128-gcm";
  return {
    send: await deriveOneSession(code, senderNonce, receiverNonce, sendInfo),
    recv: await deriveOneSession(code, senderNonce, receiverNonce, recvInfo),
    compression,
    features: [...features],
    chunkSize: features.includes(FEATURE_LARGE_CHUNKS) ? LARGE_CHUNK_SIZE : CHUNK_SIZE,
    compressionStats: { originalBytes: 0, wireBytes: 0, compressedChunks: 0 },
  };
}

async function deriveOneSession(code, senderNonce, receiverNonce, infoText) {
  return globalThis.KigoSecure.deriveSessionState(
    normalizeCode(code), senderNonce, receiverNonce, infoText,
  );
}

async function sendEncrypted(pipe, session, seq, msg) {
  const plain = UTF8_ENCODER.encode(JSON.stringify(msg));
  await sendEncryptedFrame(pipe, session, seq, plain);
}

async function sendEncryptedChunk(pipe, session, seq, msg, data) {
  if (!session.features.includes(FEATURE_BINARY_CHUNKS)) {
    await sendEncrypted(pipe, session, seq, { ...msg, data: bytesToBase64(data) });
    return;
  }
  await sendEncryptedFrame(
    pipe,
    session,
    seq,
    encodeBinaryChunk(msg, data),
    session.useDataChannel === true,
  );
}

async function sendEncryptedFrame(pipe, session, seq, plain, dataFrame = false) {
  const started = performance.now();
  const body = await globalThis.KigoSecure.encrypt(session.send, seq, plain);
  session.telemetry?.addDuration("encryptMs", performance.now() - started);
  if (session.features.includes(FEATURE_BINARY_CHUNKS)) {
    const frame = encodeBinaryEnvelope(seq, body);
    if (dataFrame) await pipe.sendData(frame);
    else await pipe.send(frame);
  } else {
    await pipe.send({ version: VERSION, seq, body: bytesToBase64(body) });
  }
}

async function recvEncrypted(pipe, session) {
  const frame = await pipe.recv();
  let env;
  let ciphertext;
  if (isBinaryEnvelope(frame)) {
    if (!session.features.includes(FEATURE_BINARY_CHUNKS)) {
      throw new Error("received binary envelope without negotiation");
    }
    env = decodeBinaryEnvelope(frame);
    ciphertext = env.body;
  } else {
    env = parseJSONFrame(frame);
    ciphertext = base64ToBytes(env.body);
  }
  validateEnvelope(env, session.recv);
  const started = performance.now();
  const plain = await globalThis.KigoSecure.decrypt(session.recv, env.seq, ciphertext);
  session.telemetry?.addDuration("decryptMs", performance.now() - started);
  const binaryChunk = decodeBinaryChunk(plain);
  if (binaryChunk && !session.features.includes(FEATURE_BINARY_CHUNKS)) {
    throw new Error("received binary chunk without negotiation");
  }
  const msg = binaryChunk || JSON.parse(UTF8_DECODER.decode(plain));
  session.recv.nextSeq++;
  return msg;
}

function isBinaryEnvelope(frame) {
  const bytes = frame instanceof ArrayBuffer
    ? new Uint8Array(frame)
    : ArrayBuffer.isView(frame)
      ? new Uint8Array(frame.buffer, frame.byteOffset, frame.byteLength)
      : null;
  return bytes !== null && hasMagic(bytes, BINARY_ENVELOPE_MAGIC);
}

function encodeBinaryEnvelope(seq, body) {
  if (!Number.isSafeInteger(seq) || seq < 0) throw new Error(`invalid envelope sequence ${seq}`);
  const out = new Uint8Array(BINARY_ENVELOPE_HEADER_BYTES + body.length);
  out.set(BINARY_ENVELOPE_MAGIC, 0);
  new DataView(out.buffer).setBigUint64(4, BigInt(seq), false);
  out.set(body, BINARY_ENVELOPE_HEADER_BYTES);
  return out;
}

function decodeBinaryEnvelope(frame) {
  const bytes = frame instanceof ArrayBuffer
    ? new Uint8Array(frame)
    : new Uint8Array(frame.buffer, frame.byteOffset, frame.byteLength);
  if (bytes.length < BINARY_ENVELOPE_HEADER_BYTES + 16 || !hasMagic(bytes, BINARY_ENVELOPE_MAGIC)) {
    throw new Error("invalid binary envelope");
  }
  const seq = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength).getBigUint64(4, false);
  if (seq > BigInt(Number.MAX_SAFE_INTEGER)) throw new Error("binary envelope sequence exceeds safe integer range");
  return { version: VERSION, seq: Number(seq), body: bytes.subarray(BINARY_ENVELOPE_HEADER_BYTES) };
}

function encodeBinaryChunk(msg, data) {
  if (!Number.isSafeInteger(msg.item) || msg.item < 0 || msg.item > 0xffffffff) {
    throw new Error(`invalid binary chunk item ${msg.item}`);
  }
  if (!Number.isSafeInteger(msg.stream) || msg.stream < 0 || msg.stream > 0xffffffff) {
    throw new Error(`invalid binary chunk stream ${msg.stream}`);
  }
  if (!Number.isSafeInteger(msg.offset) || msg.offset < 0) {
    throw new Error(`invalid binary chunk offset ${msg.offset}`);
  }
  if (!(data instanceof Uint8Array) || data.length > MAX_CHUNK_SIZE * 2) {
    throw new Error("invalid binary chunk payload");
  }
  const encoding = msg.encoding ? 1 : 0;
  if (msg.encoding && msg.encoding !== "gzip") throw new Error(`unsupported binary chunk encoding ${msg.encoding}`);
  const out = new Uint8Array(BINARY_CHUNK_HEADER_BYTES + data.length);
  const view = new DataView(out.buffer);
  out.set(BINARY_CHUNK_MAGIC, 0);
  view.setUint32(4, msg.item, false);
  view.setUint32(8, msg.stream, false);
  view.setBigUint64(12, BigInt(msg.offset), false);
  view.setUint8(20, encoding);
  out.set(data, BINARY_CHUNK_HEADER_BYTES);
  return out;
}

function decodeBinaryChunk(plain) {
  if (!hasMagic(plain, BINARY_CHUNK_MAGIC)) return null;
  if (plain.length < BINARY_CHUNK_HEADER_BYTES) throw new Error("invalid binary chunk frame");
  if (plain.length > BINARY_CHUNK_HEADER_BYTES + MAX_CHUNK_SIZE * 2) {
    throw new Error("binary chunk payload exceeds protocol limit");
  }
  const view = new DataView(plain.buffer, plain.byteOffset, plain.byteLength);
  const offset = view.getBigUint64(12, false);
  if (offset > BigInt(Number.MAX_SAFE_INTEGER)) throw new Error("binary chunk offset exceeds safe integer range");
  const encoding = view.getUint8(20);
  if (encoding > 1) throw new Error(`unsupported binary chunk encoding ${encoding}`);
  return {
    type: "chunk",
    item: view.getUint32(4, false),
    stream: view.getUint32(8, false),
    offset: Number(offset),
    data: plain.subarray(BINARY_CHUNK_HEADER_BYTES),
    encoding: encoding === 1 ? "gzip" : "",
  };
}

function hasMagic(bytes, magic) {
  if (bytes.length < magic.length) return false;
  for (let i = 0; i < magic.length; i++) {
    if (bytes[i] !== magic[i]) return false;
  }
  return true;
}

function validateEnvelope(env, recvState) {
  if (!env || env.version !== VERSION) {
    throw new Error(`unsupported envelope version ${env?.version}`);
  }
  if (!Number.isSafeInteger(env.seq) || env.seq < 0) {
    throw new Error(`invalid envelope sequence ${env.seq}`);
  }
  if (env.seq !== recvState.nextSeq) {
    throw new Error(`unexpected envelope sequence: got ${env.seq} want ${recvState.nextSeq}`);
  }
}

function negotiateURL(token, role) {
  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  const url = new URL(`${proto}//${location.host}/api/negotiate/${token}`);
  url.searchParams.set("role", role);
  return url.toString();
}

async function negotiateBrowserRoute(token, role, task, protocol = "transfer") {
  let ws = null;
  let removeCleanup = () => {};
  try {
    ws = new WebSocket(negotiateURL(token, role));
    removeCleanup = task.addCleanup(() => {
      if (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING) ws.close();
    });
    await webRTC.withTimeout(webRTC.waitWebSocket(ws), "Transport negotiation connection", SIGNAL_TIMEOUT_MS);
    if (task.canceled) throw new Error("transfer canceled");
    ws.send(JSON.stringify({
      type: "negotiate",
      version: VERSION,
      client: "web",
      protocol,
      features: protocol === "transfer"
        ? (ENABLE_PARALLEL_DATA ? [FEATURE_PARALLEL_DATA] : [])
          .concat(ENABLE_UNORDERED_DATA ? [FEATURE_UNORDERED_DATA] : [])
        : [],
    }));
    const result = await webRTC.withTimeout(waitNegotiationResult(ws), "Pairing", PAIR_TIMEOUT_MS)
      .catch((err) => { throw fatalNegotiationError(err.message); });
    if (result.type === "error") {
      throw fatalNegotiationError(result.error || "transport negotiation failed");
    }
    if (result.type !== "negotiated" || result.version !== VERSION) {
      throw fatalNegotiationError("unsupported transport negotiation response");
    }
    if (result.route !== "webrtc") {
      throw fatalNegotiationError(`browser cannot use negotiated route ${result.route}`);
    }
    log(`Route negotiation: WebRTC (${result.reason || "browser peer"})`);
    return result;
  } catch (err) {
    if (task.canceled) throw new Error("transfer canceled");
    if (err?.negotiationFatal) throw err;
    log(`Route negotiation unavailable; continuing with WebRTC (${friendlyError(err)}).`);
  } finally {
    removeCleanup();
    if (ws && (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING)) ws.close();
  }
}

function fatalNegotiationError(message) {
  const err = new Error(message);
  err.negotiationFatal = true;
  return err;
}

function waitNegotiationResult(ws) {
  return new Promise((resolve, reject) => {
    ws.addEventListener("message", (event) => {
      try {
        resolve(JSON.parse(event.data));
      } catch {
        reject(new Error("invalid transport negotiation response"));
      }
    }, { once: true });
    ws.addEventListener("error", () => reject(new Error("transport negotiation failed")), { once: true });
    ws.addEventListener("close", () => reject(new Error("transport negotiation connection closed")), { once: true });
  });
}

async function roomToken(code) {
  const input = UTF8_ENCODER.encode("kigo-room:" + normalizeCode(code));
  const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", input));
  return [...digest].map((b) => b.toString(16).padStart(2, "0")).join("");
}

function generateCode() {
  const bytes = new Uint8Array(6);
  crypto.getRandomValues(bytes);
  return [...bytes].map((b) => ALPHABET[b % ALPHABET.length]).join("");
}

function normalizeCode(code) {
  const normalized = (code || "").trim().toUpperCase();
  const compact = normalized.replace(/[-\s]/g, "");
  if (/^[A-Z0-9]{6}$/.test(compact)) return compact;
  return normalized;
}

function validPairingCode(code) {
  if (code.length < 6 || code.length > 64) return false;
  return /^[A-Z0-9]+$/.test(code) || /^[A-Z0-9]+(?:-[A-Z0-9]+)+$/.test(code);
}

function safeRelativeName(name) {
  const normalized = (name || "kigo-file").replaceAll("\\", "/");
  if (normalized.startsWith("/") || /^[A-Za-z]:/.test(normalized)) {
    throw new Error(`absolute file path in manifest: ${name}`);
  }
  const parts = normalized.split("/").filter((part) => part && part !== ".");
  if (!parts.length || parts.includes("..")) {
    throw new Error(`unsafe file path in manifest: ${name}`);
  }
  return parts.join("/");
}

function validateManifest(manifest) {
  if (!manifest || manifest.version !== VERSION) {
    throw new Error(`unsupported manifest version ${manifest?.version}`);
  }
  if (!Array.isArray(manifest.items) || manifest.items.length === 0) {
    throw new Error("manifest has no items");
  }
  const paths = new Map();
  for (let i = 0; i < manifest.items.length; i++) {
    const item = manifest.items[i];
    if (!item.name) throw new Error(`manifest item ${i} has empty name`);
    if (!Number.isSafeInteger(item.size) || item.size < 0) {
      throw new Error(`manifest item ${item.name} has invalid size ${item.size}`);
    }
    if (!Number.isSafeInteger(item.chunk_size) || item.chunk_size <= 0 || item.chunk_size > MAX_CHUNK_SIZE) {
      throw new Error(`manifest item ${item.name} has invalid chunk size ${item.chunk_size}`);
    }
    if (item.sha256 && !/^[0-9a-fA-F]{64}$/.test(item.sha256)) {
      throw new Error(`manifest item ${item.name} has invalid sha256`);
    }
    if (item.sample_sha256 && !/^[0-9a-fA-F]{64}$/.test(item.sample_sha256)) {
      throw new Error(`manifest item ${item.name} has invalid sample_sha256`);
    }
    let hasPath = false;
    if (item.kind === "file") {
      if (item.target) throw new Error(`manifest file ${item.name} has symlink target`);
      hasPath = true;
    } else if (item.kind === "directory") {
      if (item.size !== 0 || item.sha256 || item.sample_sha256 || item.target || item.resume_supported) {
        throw new Error(`manifest directory ${item.name} has file data fields`);
      }
      hasPath = true;
    } else if (item.kind === "symlink") {
      if (item.size !== 0 || item.sha256 || item.sample_sha256 || item.resume_supported) {
        throw new Error(`manifest symlink ${item.name} has file data fields`);
      }
      validateSafeSymlinkTarget(item.name, item.target);
      hasPath = true;
    } else if (item.kind === "text") {
      if (item.sample_sha256) throw new Error(`manifest text ${item.name} has sample hash`);
    } else {
      throw new Error(`unsupported item kind ${item.kind}`);
    }
    if (hasPath) {
      const safeName = safeRelativeName(item.name);
      if (paths.has(safeName)) throw new Error(`duplicate path in manifest: ${item.name}`);
      paths.set(safeName, item.kind);
    }
  }
  for (const name of paths.keys()) {
    const parts = name.split("/");
    for (let n = 1; n < parts.length; n++) {
      const parent = parts.slice(0, n).join("/");
      if (paths.has(parent) && paths.get(parent) !== "directory") {
        throw new Error(`manifest path ${name} has non-directory parent ${parent}`);
      }
    }
  }
  return createMuxPlan(manifest);
}

function validateSafeSymlinkTarget(name, target) {
  if (typeof target !== "string" || !target || target.includes("\0")) {
    throw new Error(`refusing empty symlink target for ${name}`);
  }
  const normalized = target.replaceAll("\\", "/");
  if (normalized.startsWith("/") || /^[A-Za-z]:/.test(normalized)) {
    throw new Error(`refusing absolute symlink target for ${name}: ${target}`);
  }
  if (normalized.split("/").includes("..")) {
    throw new Error(`refusing unsafe symlink target for ${name}: ${target}`);
  }
}

function createMuxPlan(manifest) {
  if (!manifest || !Array.isArray(manifest.items) || manifest.items.length === 0) {
    throw new Error("manifest has no items");
  }
  const bindings = Array.isArray(manifest.streams) && manifest.streams.length
    ? manifest.streams
    : manifest.items.map((_, item) => ({ id: item, item }));
  if (bindings.length !== manifest.items.length) {
    throw new Error(`manifest stream count ${bindings.length} does not match item count ${manifest.items.length}`);
  }
  const byItem = new Map();
  const byStream = new Map();
  for (const binding of bindings) {
    if (!Number.isSafeInteger(binding.id) || binding.id < 0) {
      throw new Error(`invalid stream id: ${binding.id}`);
    }
    if (!Number.isSafeInteger(binding.item) || binding.item < 0 || binding.item >= manifest.items.length) {
      throw new Error(`stream ${binding.id} item index out of range: ${binding.item}`);
    }
    if (byStream.has(binding.id)) throw new Error(`duplicate stream id in manifest: ${binding.id}`);
    if (byItem.has(binding.item)) throw new Error(`duplicate stream binding for item: ${binding.item}`);
    byStream.set(binding.id, binding.item);
    byItem.set(binding.item, binding.id);
  }
  for (let item = 0; item < manifest.items.length; item++) {
    if (!byItem.has(item)) throw new Error(`manifest item ${item} has no stream binding`);
  }
  return {
    bindings: bindings.map((binding) => ({ id: binding.id, item: binding.item })),
    streamForItem(item) {
      if (!byItem.has(item)) throw new Error(`manifest item ${item} has no stream binding`);
      return byItem.get(item);
    },
    itemForStream(stream) {
      if (!byStream.has(stream)) throw new Error(`unknown stream id: ${stream}`);
      return byStream.get(stream);
    },
  };
}

function createWeightedScheduler(streams, quantum) {
  if (!Number.isSafeInteger(quantum) || quantum <= 0) {
    throw new Error(`scheduler quantum must be positive: ${quantum}`);
  }
  const states = [];
  const byID = new Map();
  for (const stream of streams) {
    if (!Number.isSafeInteger(stream.id) || stream.id < 0) {
      throw new Error(`invalid scheduler stream id: ${stream.id}`);
    }
    if (!Number.isSafeInteger(stream.weight) || stream.weight <= 0) {
      throw new Error(`stream ${stream.id} has invalid weight ${stream.weight}`);
    }
    if (byID.has(stream.id)) throw new Error(`duplicate scheduler stream id: ${stream.id}`);
    const state = { id: stream.id, weight: stream.weight, deficit: 0, active: true };
    states.push(state);
    byID.set(state.id, state);
  }
  let cursor = 0;
  let active = states.length;
  let current = null;
  let budget = 0;

  function advance() {
    if (states.length) cursor = (cursor + 1) % states.length;
  }

  return {
    next(maxBytes) {
      if (!Number.isSafeInteger(maxBytes) || maxBytes <= 0) {
        throw new Error(`maximum turn size must be positive: ${maxBytes}`);
      }
      if (current) throw new Error(`stream ${current.id} turn has not been committed`);
      if (active === 0) return null;
      for (;;) {
        const state = states[cursor];
        if (!state.active) {
          advance();
          continue;
        }
        if (state.deficit <= 0) state.deficit += quantum * state.weight;
        budget = Math.min(maxBytes, state.deficit);
        current = state;
        return { streamID: state.id, budget };
      }
    },
    commit(streamID, usedBytes, done) {
      if (!current) throw new Error("scheduler has no active turn");
      if (current.id !== streamID) {
        throw new Error(`committed stream ${streamID} does not match active stream ${current.id}`);
      }
      if (!Number.isSafeInteger(usedBytes) || usedBytes < 0 || usedBytes > budget) {
        throw new Error(`stream ${streamID} used ${usedBytes} bytes outside turn budget ${budget}`);
      }
      if (usedBytes === 0 && !done) throw new Error(`stream ${streamID} made no progress`);
      current.deficit -= usedBytes;
      if (done) {
        current.active = false;
        current.deficit = 0;
        active--;
      }
      const shouldAdvance = done || current.deficit <= 0 || usedBytes < budget;
      current = null;
      budget = 0;
      if (shouldAdvance && active > 0) advance();
    },
  };
}

function fileStreamWeight(remaining) {
  if (remaining <= 1 << 20) return 4;
  if (remaining <= 16 << 20) return 2;
  return 1;
}

function downloadName(name) {
  const parts = safeRelativeName(name).split("/");
  return parts[parts.length - 1] || "kigo-file";
}

function concat(parts) {
  const size = parts.reduce((sum, p) => sum + p.length, 0);
  const out = new Uint8Array(size);
  let offset = 0;
  for (const part of parts) {
    out.set(part, offset);
    offset += part.length;
  }
  return out;
}

async function encodeTransferChunk(data, session, state) {
  session.compressionStats.originalBytes += data.length;
  if (session.compression !== "gzip" || state.disabled) {
    session.compressionStats.wireBytes += data.length;
    return { data, encoding: "" };
  }
  const started = performance.now();
  const encoded = await compressGzipChunk(data);
  session.telemetry?.addDuration("compressMs", performance.now() - started);
  state.attempts = (state.attempts || 0) + 1;
  if (compressionWorthwhile(data.length, encoded.length)) {
    session.compressionStats.wireBytes += encoded.length;
    session.compressionStats.compressedChunks++;
    return { data: encoded, encoding: "gzip" };
  }
  state.misses = (state.misses || 0) + 1;
  if (state.attempts >= 3 && state.misses === state.attempts) state.disabled = true;
  session.compressionStats.wireBytes += data.length;
  return { data, encoding: "" };
}

async function decodeTransferChunk(data, encoding, negotiated) {
  const compression = typeof negotiated === "string" ? negotiated : negotiated?.compression;
  const telemetry = typeof negotiated === "string" ? null : negotiated?.telemetry;
  if (!encoding) return data;
  if (encoding !== "gzip") throw new Error(`unsupported chunk encoding ${encoding}`);
  if (compression !== "gzip") throw new Error("received gzip chunk without negotiated compression");
  const started = performance.now();
  const decoded = await decompressGzipChunk(data);
  telemetry?.addDuration("decompressMs", performance.now() - started);
  return decoded;
}

async function compressGzipChunk(data) {
  const stream = new Blob([data]).stream().pipeThrough(new CompressionStream("gzip"));
  return new Uint8Array(await new Response(stream).arrayBuffer());
}

async function decompressGzipChunk(data) {
  if (data.length > MAX_CHUNK_SIZE * 2) throw new Error("compressed chunk exceeds encoded size limit");
  let stream;
  try {
    stream = new Blob([data]).stream().pipeThrough(new DecompressionStream("gzip"));
  } catch (err) {
    throw new Error(`invalid gzip chunk: ${err.message}`);
  }
  const reader = stream.getReader();
  const parts = [];
  let size = 0;
  try {
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      size += value.length;
      if (size > MAX_CHUNK_SIZE) {
        await reader.cancel();
        throw new Error("decompressed chunk exceeds protocol chunk size");
      }
      parts.push(value);
    }
  } catch (err) {
    if (err.message?.includes("decompressed chunk exceeds")) throw err;
    throw new Error(`invalid gzip chunk: ${err.message}`);
  }
  return concat(parts);
}

function compressionWorthwhile(original, encoded) {
  if (original < 256) return false;
  return encoded + Math.max(32, Math.floor(original / 100)) < original;
}

function logCompressionStats(session) {
  const stats = session.compressionStats;
  if (!stats || stats.compressedChunks === 0 || stats.originalBytes === 0) return;
  const saved = Math.max(0, stats.originalBytes - stats.wireBytes);
  const percent = Math.round(saved * 100 / stats.originalBytes);
  log(`Compressed ${formatBytes(stats.originalBytes)} to ${formatBytes(stats.wireBytes)} (${percent}% saved).`);
}

function makeZip(entries) {
  const chunks = [];
  const central = [];
  let offset = 0;
  for (const entry of entries) {
    let name = safeRelativeName(entry.name);
    if (entry.kind === "directory") name += "/";
    const parts = entry.parts || [];
    const nameBytes = UTF8_ENCODER.encode(name);
    const size = parts.reduce((sum, part) => sum + part.length, 0);
    const crc = crc32Parts(parts);
    const { time, date } = zipDosDateTime(entry.mtime);
    const local = new Uint8Array(30 + nameBytes.length);
    const localView = new DataView(local.buffer);
    localView.setUint32(0, 0x04034b50, true);
    localView.setUint16(4, 20, true);
    localView.setUint16(6, 0x0800, true);
    localView.setUint16(8, 0, true);
    localView.setUint16(10, time, true);
    localView.setUint16(12, date, true);
    localView.setUint32(14, crc, true);
    localView.setUint32(18, size, true);
    localView.setUint32(22, size, true);
    localView.setUint16(26, nameBytes.length, true);
    localView.setUint16(28, 0, true);
    local.set(nameBytes, 30);
    chunks.push(local, ...parts);

    const centralHeader = new Uint8Array(46 + nameBytes.length);
    const centralView = new DataView(centralHeader.buffer);
    centralView.setUint32(0, 0x02014b50, true);
    centralView.setUint16(4, (3 << 8) | 20, true);
    centralView.setUint16(6, 20, true);
    centralView.setUint16(8, 0x0800, true);
    centralView.setUint16(10, 0, true);
    centralView.setUint16(12, time, true);
    centralView.setUint16(14, date, true);
    centralView.setUint32(16, crc, true);
    centralView.setUint32(20, size, true);
    centralView.setUint32(24, size, true);
    centralView.setUint16(28, nameBytes.length, true);
    centralView.setUint16(30, 0, true);
    centralView.setUint16(32, 0, true);
    centralView.setUint16(34, 0, true);
    centralView.setUint16(36, 0, true);
    centralView.setUint32(38, zipExternalAttributes(entry), true);
    centralView.setUint32(42, offset, true);
    centralHeader.set(nameBytes, 46);
    central.push(centralHeader);
    offset += local.length + size;
  }
  const centralOffset = offset;
  const centralSize = central.reduce((sum, chunk) => sum + chunk.length, 0);
  chunks.push(...central);
  const end = new Uint8Array(22);
  const endView = new DataView(end.buffer);
  endView.setUint32(0, 0x06054b50, true);
  endView.setUint16(4, 0, true);
  endView.setUint16(6, 0, true);
  endView.setUint16(8, entries.length, true);
  endView.setUint16(10, entries.length, true);
  endView.setUint32(12, centralSize, true);
  endView.setUint32(16, centralOffset, true);
  endView.setUint16(20, 0, true);
  chunks.push(end);
  return chunks;
}

function zipExternalAttributes(entry) {
  const defaultMode = entry.kind === "directory" ? 0o755 : entry.kind === "symlink" ? 0o777 : 0o644;
  const mode = Number.isSafeInteger(entry.mode) && entry.mode > 0 ? entry.mode & 0o777 : defaultMode;
  const type = entry.kind === "directory" ? 0x4000 : entry.kind === "symlink" ? 0xa000 : 0x8000;
  const dosFlags = entry.kind === "directory" ? 0x10 : 0;
  return ((((type | mode) & 0xffff) << 16) | dosFlags) >>> 0;
}

function zipDosDateTime(millis) {
  const value = Number.isSafeInteger(millis) && millis > 0 ? new Date(millis) : new Date();
  const year = Math.min(2107, Math.max(1980, value.getFullYear()));
  return {
    time: ((value.getHours() & 0x1f) << 11) | ((value.getMinutes() & 0x3f) << 5) | ((Math.floor(value.getSeconds() / 2)) & 0x1f),
    date: ((year - 1980) << 9) | (((value.getMonth() + 1) & 0x0f) << 5) | (value.getDate() & 0x1f),
  };
}

function crc32Parts(parts) {
  let crc = 0xffffffff;
  for (const bytes of parts) {
    for (const byte of bytes) {
      crc = (crc >>> 8) ^ CRC32_TABLE[(crc ^ byte) & 0xff];
    }
  }
  return (crc ^ 0xffffffff) >>> 0;
}

const CRC32_TABLE = (() => {
  const table = new Uint32Array(256);
  for (let i = 0; i < 256; i++) {
    let c = i;
    for (let j = 0; j < 8; j++) {
      c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    }
    table[i] = c >>> 0;
  }
  return table;
})();

async function fileSHA256(file, onProgress = null) {
  const hasher = new SHA256();
  const hashChunkSize = 4 * 1024 * 1024;
  for (let offset = 0; offset < file.size; offset += hashChunkSize) {
    const chunk = new Uint8Array(await file.slice(offset, offset + hashChunkSize).arrayBuffer());
    hasher.update(chunk);
    if (onProgress) onProgress(Math.min(file.size, offset + chunk.length));
  }
  return hex(hasher.digest());
}

async function fileSampleSHA256(file) {
  const chunkSize = 128 * 1024;
  const sampleWindow = 1024 * 1024;
  const maxSampleBytes = 1024 * 1024;
  if (file.size <= chunkSize * 2) return fileSHA256(file);
  const hasher = new SHA256();
  const updateRange = async (offset) => {
    hasher.update(new Uint8Array(await file.slice(offset, offset + chunkSize).arrayBuffer()));
  };
  await updateRange(0);
  await updateRange(file.size - chunkSize);
  let sampled = chunkSize * 2;
  const windows = Math.max(1, Math.floor((file.size - chunkSize) / sampleWindow));
  for (let window = 0; window < windows && sampled < maxSampleBytes; window++) {
    const offset = window * sampleWindow + sampleWindow / 2;
    if (offset + chunkSize > file.size) break;
    await updateRange(offset);
    sampled += chunkSize;
  }
  return hex(hasher.digest());
}

async function filePrefixSHA256(file, size) {
  if (!Number.isSafeInteger(size) || size < 0 || size > file.size) {
    throw new Error(`invalid file prefix size ${size}`);
  }
  return fileSHA256(file.slice(0, size));
}

async function bytesSHA256(bytes) {
  const hasher = new SHA256();
  hasher.update(bytes);
  return hex(hasher.digest());
}

async function verifyItemBytes(item, bytes) {
  return verifyItemParts(item, [bytes]);
}

function validateChunkBounds(item, offset, dataLen, expectedOffset) {
  if (offset < 0) {
    throw new Error(`negative chunk offset for ${item.name}: ${offset}`);
  }
  if (offset !== expectedOffset) {
    throw new Error(`unexpected chunk offset for ${item.name}: got ${offset} want ${expectedOffset}`);
  }
  const end = offset + dataLen;
  if (end < offset || end > item.size) {
    throw new Error(`chunk exceeds declared size for ${item.name}: end ${end} size ${item.size}`);
  }
}

function createStreamTracker(plan = null) {
  const states = new Map();
  return {
    acceptOpen(manifest, msg) {
      return acceptStreamOpen(manifest, states, msg, plan);
    },
    acceptEnd(manifest, msg) {
      return acceptStreamEnd(manifest, states, msg, plan);
    },
    acceptChunk(manifest, msg) {
      return acceptChunkStream(manifest, states, msg, plan);
    },
  };
}

function acceptStreamOpen(manifest, streamStates, msg, plan = createMuxPlan(manifest)) {
  const binding = validateStreamFrame(manifest, msg, true, plan);
  const state = streamStates.get(binding.id) || {};
  if (state.opened) throw new Error(`stream ${binding.id} opened more than once`);
  streamStates.set(binding.id, { item: binding.item, opened: true, ended: false });
  return binding;
}

function acceptStreamEnd(manifest, streamStates, msg, plan = createMuxPlan(manifest)) {
  const binding = validateStreamFrame(manifest, msg, true, plan);
  const state = streamStates.get(binding.id) || {};
  if (!state.opened) throw new Error(`stream ${binding.id} ended before open`);
  if (state.ended) throw new Error(`stream ${binding.id} ended more than once`);
  streamStates.set(binding.id, { item: binding.item, opened: true, ended: true });
  return binding;
}

function acceptChunkStream(manifest, streamStates, msg, plan = createMuxPlan(manifest)) {
  const binding = validateStreamFrame(manifest, msg, false, plan);
  const state = streamStates.get(binding.id) || {};
  if (state.ended) throw new Error(`chunk arrived after stream ${binding.id} ended`);
  return binding;
}

function validateResumeEntry(manifestOrItems, entry, plan = null) {
  const manifest = Array.isArray(manifestOrItems)
    ? { version: VERSION, items: manifestOrItems }
    : manifestOrItems;
  const items = manifest.items;
  const streamPlan = plan || createMuxPlan(manifest);
  if (!Number.isSafeInteger(entry.item) || entry.item < 0 || entry.item >= items.length) {
    throw new Error(`resume item index out of range: ${entry.item}`);
  }
  const expectedStream = streamPlan.streamForItem(entry.item);
  if (entry.stream !== undefined && entry.stream !== null && (!Number.isSafeInteger(entry.stream) || entry.stream !== expectedStream)) {
    throw new Error(`resume stream ${entry.stream} does not match item ${entry.item} binding ${expectedStream}`);
  }
  if (!Number.isSafeInteger(entry.offset) || entry.offset < 0) {
    throw new Error(`negative resume offset for ${items[entry.item].name}: ${entry.offset}`);
  }
  if (entry.prefix_sha256 && !/^[0-9a-fA-F]{64}$/.test(entry.prefix_sha256)) {
    throw new Error(`invalid resume prefix sha256 for ${items[entry.item].name}`);
  }
  if (entry.sha256 && !/^[0-9a-fA-F]{64}$/.test(entry.sha256)) {
    throw new Error(`invalid resume sha256 for ${items[entry.item].name}`);
  }
  if (entry.complete && !entry.skip) {
    throw new Error(`completed resume for ${items[entry.item].name} must be a skip`);
  }
  if (entry.skip) {
    if (items[entry.item].kind !== "file") {
      throw new Error(`skip requested for non-file item ${items[entry.item].name}`);
    }
    if (entry.offset !== items[entry.item].size) {
      throw new Error(`skip offset for ${items[entry.item].name} must equal file size`);
    }
    if (entry.prefix_sha256) {
      throw new Error(`skip request for ${items[entry.item].name} must not include prefix sha256`);
    }
  }
  return clampNumber(entry.offset, 0, items[entry.item].size);
}

function applyAcceptedFileSHA256(manifest, entry, session) {
  const item = manifest.items[entry.item];
  if (!item || item.kind !== "file" || entry.skip) return;
  const accepted = String(entry.sha256 || "").toLowerCase();
  if (session.features.includes(FEATURE_DEFERRED_FILE_SHA256) && !accepted) {
    throw new Error(`resume_accept missing deferred sha256 for ${item.name}`);
  }
  if (!accepted) return;
  if (item.sha256 && item.sha256.toLowerCase() !== accepted) {
    throw new Error(`resume_accept sha256 for ${item.name} conflicts with manifest`);
  }
  item.sha256 = accepted;
}

function validateStreamFrame(manifest, msg, requireStream, plan = createMuxPlan(manifest)) {
  if (!Number.isSafeInteger(msg.item) || msg.item < 0 || msg.item >= manifest.items.length) {
    throw new Error(`chunk item index out of range: ${msg.item}`);
  }
  if (requireStream && (msg.stream === undefined || msg.stream === null)) {
    throw new Error(`${msg.type} missing stream`);
  }
  const stream = validateChunkStream(msg, plan);
  return { id: stream, item: msg.item };
}

function validateChunkStream(msg, plan = null) {
  const expectedStream = plan ? plan.streamForItem(msg.item) : msg.item;
  if (msg.stream === undefined || msg.stream === null) return expectedStream;
  if (!Number.isSafeInteger(msg.stream) || msg.stream !== expectedStream) {
    throw new Error(`chunk stream ${msg.stream} does not match item ${msg.item} binding ${expectedStream}`);
  }
  return msg.stream;
}

async function verifyItemParts(item, parts) {
  const size = parts.reduce((sum, part) => sum + part.length, 0);
  if (size !== item.size) {
    throw new Error(`size mismatch for ${item.name}: got ${size}, want ${item.size}`);
  }
  if (item.sha256) {
    const hasher = new SHA256();
    for (const part of parts) hasher.update(part);
    const got = hex(hasher.digest());
    if (got !== item.sha256.toLowerCase()) {
      throw new Error(`sha256 mismatch for ${item.name}`);
    }
  }
}

async function verifyItemFile(item, file) {
  if (file.size !== item.size) {
    throw new Error(`size mismatch for ${item.name}: got ${file.size}, want ${item.size}`);
  }
  if (item.sha256) {
    const got = await fileSHA256(file);
    if (got !== item.sha256.toLowerCase()) {
      throw new Error(`sha256 mismatch for ${item.name}`);
    }
  }
}

function hex(bytes) {
  return [...bytes].map((b) => b.toString(16).padStart(2, "0")).join("");
}

class SHA256 {
  constructor() {
    this.state = new Uint32Array([
      0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a,
      0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19,
    ]);
    this.buffer = new Uint8Array(64);
    this.bufferLength = 0;
    this.bytesHashed = 0n;
    this.temp = new Uint32Array(64);
    this.finished = false;
  }

  update(data) {
    if (this.finished) throw new Error("sha256 already finalized");
    if (!(data instanceof Uint8Array)) data = new Uint8Array(data);
    this.bytesHashed += BigInt(data.length);
    let offset = 0;
    while (offset < data.length) {
      const want = Math.min(data.length - offset, 64 - this.bufferLength);
      this.buffer.set(data.subarray(offset, offset + want), this.bufferLength);
      this.bufferLength += want;
      offset += want;
      if (this.bufferLength === 64) {
        this.processBlock(this.buffer, 0);
        this.bufferLength = 0;
      }
    }
    return this;
  }

  digest() {
    if (this.finished) throw new Error("sha256 already finalized");
    this.finished = true;
    const bitLength = this.bytesHashed * 8n;
    this.buffer[this.bufferLength++] = 0x80;
    if (this.bufferLength > 56) {
      this.buffer.fill(0, this.bufferLength, 64);
      this.processBlock(this.buffer, 0);
      this.bufferLength = 0;
    }
    this.buffer.fill(0, this.bufferLength, 56);
    let n = bitLength;
    for (let i = 63; i >= 56; i--) {
      this.buffer[i] = Number(n & 0xffn);
      n >>= 8n;
    }
    this.processBlock(this.buffer, 0);
    const out = new Uint8Array(32);
    const view = new DataView(out.buffer);
    for (let i = 0; i < 8; i++) view.setUint32(i * 4, this.state[i], false);
    return out;
  }

  processBlock(block, offset) {
    const w = this.temp;
    for (let i = 0; i < 16; i++) {
      const j = offset + i * 4;
      w[i] = ((block[j] << 24) | (block[j + 1] << 16) | (block[j + 2] << 8) | block[j + 3]) >>> 0;
    }
    for (let i = 16; i < 64; i++) {
      const s0 = rotr(w[i - 15], 7) ^ rotr(w[i - 15], 18) ^ (w[i - 15] >>> 3);
      const s1 = rotr(w[i - 2], 17) ^ rotr(w[i - 2], 19) ^ (w[i - 2] >>> 10);
      w[i] = (w[i - 16] + s0 + w[i - 7] + s1) >>> 0;
    }

    let a = this.state[0];
    let b = this.state[1];
    let c = this.state[2];
    let d = this.state[3];
    let e = this.state[4];
    let f = this.state[5];
    let g = this.state[6];
    let h = this.state[7];

    for (let i = 0; i < 64; i++) {
      const s1 = rotr(e, 6) ^ rotr(e, 11) ^ rotr(e, 25);
      const ch = (e & f) ^ (~e & g);
      const t1 = (h + s1 + ch + SHA256_K[i] + w[i]) >>> 0;
      const s0 = rotr(a, 2) ^ rotr(a, 13) ^ rotr(a, 22);
      const maj = (a & b) ^ (a & c) ^ (b & c);
      const t2 = (s0 + maj) >>> 0;
      h = g;
      g = f;
      f = e;
      e = (d + t1) >>> 0;
      d = c;
      c = b;
      b = a;
      a = (t1 + t2) >>> 0;
    }

    this.state[0] = (this.state[0] + a) >>> 0;
    this.state[1] = (this.state[1] + b) >>> 0;
    this.state[2] = (this.state[2] + c) >>> 0;
    this.state[3] = (this.state[3] + d) >>> 0;
    this.state[4] = (this.state[4] + e) >>> 0;
    this.state[5] = (this.state[5] + f) >>> 0;
    this.state[6] = (this.state[6] + g) >>> 0;
    this.state[7] = (this.state[7] + h) >>> 0;
  }
}

function rotr(x, n) {
  return (x >>> n) | (x << (32 - n));
}

const SHA256_K = new Uint32Array([
  0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
  0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
  0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
  0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
  0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
  0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
  0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
  0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
]);

function showShare(code) {
  const search = ENABLE_PARALLEL_DATA ? "?parallel=1" : "";
  const link = `${location.origin}/${search}#c=${code}`;
  shareEl.innerHTML = `<strong>Code:</strong> ${code}<br><strong>Link:</strong> <a href="${link}">${link}</a>`;
  shareEl.classList.remove("hidden");
}

function resetOutput() {
  logEl.textContent = "";
  downloads.textContent = "";
  textOutput.textContent = "";
  textOutput.classList.add("hidden");
  shareEl.classList.add("hidden");
  progressEl.classList.add("hidden");
  progressEl.textContent = "";
  window.__kigoLastTransferMetrics = null;
}

function log(message) {
  logEl.textContent += `${new Date().toLocaleTimeString()} ${message}\n`;
}

function createChunkProgressLogger(action) {
  const lastLoggedAt = new Map();
  return ({ itemName, offset, bytes, total, complete = false }) => {
    const now = performance.now();
    const last = lastLoggedAt.get(itemName);
    if (!complete && last !== undefined && now - last < CHUNK_LOG_INTERVAL_MS) return;
    lastLoggedAt.set(itemName, now);
    if (action === "received") {
      log(`${action} ${itemName} offset=${offset} bytes=${bytes}`);
    } else {
      log(`${action} ${itemName} ${offset}/${total} bytes`);
    }
  };
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function createProgress(label, total) {
  const started = performance.now();
  let lastRenderedAt = -Infinity;
  progressEl.classList.remove("hidden");
  progressEl.innerHTML = `
    <div class="progress-head">
      <span class="progress-title"></span>
      <span class="progress-percent"></span>
    </div>
    <div class="progress-track"><div class="progress-fill"></div></div>
    <div class="progress-meta">
      <span class="progress-bytes"></span>
      <span class="progress-rate"></span>
    </div>
  `;
  const titleEl = progressEl.querySelector(".progress-title");
  const percentEl = progressEl.querySelector(".progress-percent");
  const fillEl = progressEl.querySelector(".progress-fill");
  const bytesEl = progressEl.querySelector(".progress-bytes");
  const rateEl = progressEl.querySelector(".progress-rate");
  const safeTotal = Math.max(0, total || 0);
  return {
    update(done, itemName = "") {
      const safeDone = Math.min(Math.max(0, done || 0), safeTotal);
      const now = performance.now();
      if (safeDone < safeTotal && now - lastRenderedAt < PROGRESS_RENDER_INTERVAL_MS) return;
      lastRenderedAt = now;
      const ratio = safeTotal === 0 ? 1 : safeDone / safeTotal;
      const percent = Math.round(ratio * 100);
      const seconds = Math.max(0.001, (now - started) / 1000);
      titleEl.textContent = itemName ? `${label}: ${itemName}` : label;
      percentEl.textContent = `${percent}%`;
      fillEl.style.width = `${percent}%`;
      bytesEl.textContent = `${formatBytes(safeDone)} / ${formatBytes(safeTotal)}`;
      rateEl.textContent = `${formatBytes(safeDone / seconds)}/s`;
    },
  };
}

function createStreamProgress(label, manifestOrItems, initialOffsets = new Map()) {
  const manifest = Array.isArray(manifestOrItems)
    ? { version: VERSION, items: manifestOrItems }
    : manifestOrItems;
  const items = manifest.items;
  const plan = createMuxPlan(manifest);
  const progress = createProgress(label, totalManifestBytes(items));
  const streams = new Map();
  let done = 0;
  items.forEach((item, itemID) => {
    const streamID = plan.streamForItem(itemID);
    const size = Math.max(0, item.size || 0);
    const streamDone = clampNumber(initialOffsets.get(itemID) || 0, 0, size);
    streams.set(streamID, {
      name: item.name || `stream ${streamID}`,
      size,
      done: streamDone,
    });
    done += streamDone;
  });
  done = clampNumber(done, 0, totalManifestBytes(items));
  return {
    show(labelOverride = "") {
      progress.update(done, labelOverride);
    },
    addStream(streamID, delta) {
      const stream = streams.get(streamID);
      if (!stream) {
        done = clampNumber(done + delta, 0, totalManifestBytes(items));
        progress.update(done);
        return;
      }
      const before = stream.done;
      stream.done = clampNumber(stream.done + delta, 0, stream.size);
      done = clampNumber(done + (stream.done - before), 0, totalManifestBytes(items));
      progress.update(done, stream.name);
    },
  };
}

function totalItemBytes(items) {
  return items.reduce((sum, item) => {
    if (item.kind === "file") return sum + item.file.size;
    return sum + UTF8_ENCODER.encode(item.text).length;
  }, 0);
}

function totalManifestBytes(items) {
  return items.reduce((sum, item) => sum + Math.max(0, item.size || 0), 0);
}

function clampNumber(value, min, max) {
  return Math.min(Math.max(value, min), max);
}

function formatBytes(bytes) {
  const units = ["B", "KB", "MB", "GB", "TB"];
  let value = Math.max(0, bytes || 0);
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit++;
  }
  const digits = value >= 100 || unit === 0 ? 0 : value >= 10 ? 1 : 2;
  return `${value.toFixed(digits)} ${units[unit]}`;
}
