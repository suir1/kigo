#!/usr/bin/env node

const crypto = require("crypto");
const fs = require("fs");
const os = require("os");
const path = require("path");

const ALPHABET = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789";
const MEMORY_RECOVERY_LIMIT_BYTES = 128 * 1024 * 1024;
const RESUME_CHECKPOINT_BYTES = 4 * 1024 * 1024;

function parseArgs(argv) {
  const out = { dryRun: false };
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    if (arg === "--dry-run") {
      out.dryRun = true;
      continue;
    }
    if (!arg.startsWith("--") || i + 1 >= argv.length) throw new Error(`invalid argument ${arg}`);
    out[arg.slice(2).replaceAll("-", "_")] = argv[++i];
  }
  return out;
}

function validateOptions(raw) {
  let url;
  try {
    url = new URL(raw.url || "");
  } catch {
    throw new Error("KIGO_PUBLIC_BROWSER_URL must be an absolute URL");
  }
  const loopback = ["127.0.0.1", "::1", "localhost"].includes(url.hostname);
  if (url.protocol !== "https:" && !(url.protocol === "http:" && loopback)) {
    throw new Error("public browser URL must use HTTPS; HTTP is allowed only for loopback");
  }
  if (url.username || url.password || url.search || url.hash) {
    throw new Error("public browser URL must not contain credentials, query, or fragment");
  }
  if (!['chromium', 'firefox', 'webkit'].includes(raw.engine)) {
    throw new Error("browser engine must be chromium, firefox, or webkit");
  }
  if (!['0', '1'].includes(raw.force_turn)) throw new Error("force TURN must be 0 or 1");
  if (!['0', '1'].includes(raw.ignore_tls_errors)) throw new Error("ignore TLS errors must be 0 or 1");
  const scenarios = String(raw.scenarios || "").split(",").map((value) => value.trim()).filter(Boolean);
  if (!scenarios.length || scenarios.some((value) => !['text', 'file', 'resume'].includes(value))) {
    throw new Error("scenarios must be a comma-separated subset of text,file,resume");
  }
  const timeoutSeconds = Number(raw.timeout_seconds);
  if (!Number.isInteger(timeoutSeconds) || timeoutSeconds < 1 || timeoutSeconds > 600) {
    throw new Error("timeout seconds must be an integer between 1 and 600");
  }
  const configuredFilePath = String(process.env.KIGO_PUBLIC_BROWSER_FILE_PATH || "").trim();
  let fileBytes = Number(process.env.KIGO_PUBLIC_BROWSER_FILE_BYTES || 256 * 1024);
  if (configuredFilePath) {
    let stat;
    try {
      stat = fs.statSync(configuredFilePath);
    } catch (err) {
      throw new Error(`KIGO_PUBLIC_BROWSER_FILE_PATH is not readable: ${err.message}`);
    }
    if (!stat.isFile()) throw new Error("KIGO_PUBLIC_BROWSER_FILE_PATH must point to a regular file");
    if (!Number.isSafeInteger(stat.size) || stat.size < 1 || stat.size > 512 * 1024 * 1024) {
      throw new Error("KIGO_PUBLIC_BROWSER_FILE_PATH must be between 1 and 536870912 bytes");
    }
    fileBytes = stat.size;
  }
  if (!Number.isSafeInteger(fileBytes) || fileBytes < 1 || fileBytes > 512 * 1024 * 1024) {
    throw new Error("KIGO_PUBLIC_BROWSER_FILE_BYTES must be an integer between 1 and 536870912");
  }
  if (scenarios.includes("resume") && fileBytes <= RESUME_CHECKPOINT_BYTES) {
    throw new Error(`resume scenario requires KIGO_PUBLIC_BROWSER_FILE_BYTES greater than ${RESUME_CHECKPOINT_BYTES}`);
  }
  return {
    url: url.origin + url.pathname.replace(/\/$/, ""),
    engine: raw.engine,
    channel: raw.channel || "",
    forceTurn: raw.force_turn === '1',
    ignoreTLSErrors: raw.ignore_tls_errors === '1',
    scenarios,
    timeoutMS: timeoutSeconds * 1000,
    fileBytes,
    filePath: configuredFilePath,
    artifactDir: path.resolve(raw.artifact_dir || "artifacts/public-browser-matrix"),
    dryRun: raw.dryRun,
  };
}

function randomCode() {
  return Array.from(crypto.randomBytes(6), (byte) => ALPHABET[byte % ALPHABET.length]).join("");
}

function sanitize(text, code = "") {
  let out = String(text || "");
  if (code) out = out.replaceAll(code, "[REDACTED_CODE]");
  return out
    .replace(/(#(?:c|n)=)[A-HJ-NP-Z2-9]{6}/g, "$1[REDACTED_CODE]")
    .replace(/\b[0-9a-f]{64}\b/gi, "[REDACTED_ROOM_TOKEN]");
}

function sha256(file) {
  return crypto.createHash("sha256").update(fs.readFileSync(file)).digest("hex");
}

function finiteMetric(value, digits = 3) {
  if (!Number.isFinite(value)) return 0;
  return Number(value.toFixed(digits));
}

function summarizeTransferMetrics(metrics) {
  if (!metrics || typeof metrics !== "object") return null;
  const storage = metrics.storage && typeof metrics.storage === "object" ? metrics.storage : {};
  return {
    role: String(metrics.role || ""),
    payload_bytes: finiteMetric(metrics.payloadBytes, 0),
    payload_mib_per_second: finiteMetric(metrics.payloadMiBPerSecond),
    rtc_mib_per_second: finiteMetric(metrics.rtcMiBPerSecond),
    route: String(metrics.route || ""),
    local_candidate_type: String(metrics.localCandidateType || "unknown"),
    remote_candidate_type: String(metrics.remoteCandidateType || "unknown"),
    protocol: String(metrics.protocol || "unknown"),
    path_count: finiteMetric(metrics.pathCount, 0),
    rtt_ms: finiteMetric(metrics.rttMs),
    max_buffered_bytes: finiteMetric(metrics.maxBufferedBytes, 0),
    send_wait_ms: finiteMetric(metrics.sendWaitMs),
    storage: {
      type: String(storage.type || ""),
      mode: String(storage.mode || ""),
      write_ms: finiteMetric(storage.writeMs),
      queue_wait_ms: finiteMetric(storage.queueWaitMs),
      max_queued_bytes: finiteMetric(storage.maxQueuedBytes, 0),
      checkpoint_ms: finiteMetric(storage.checkpointMs),
    },
  };
}

function assertLargeFileStorage(options, receiverMetrics, receiverLog) {
  if (options.engine !== "chromium" || options.fileBytes <= MEMORY_RECOVERY_LIMIT_BYTES) return false;
  if (receiverMetrics?.storage?.type !== "opfs") {
    throw new Error(`large Chromium receive did not use OPFS storage (got ${receiverMetrics?.storage?.type || "unavailable"})`);
  }
  if (/memory recovery/i.test(String(receiverLog || ""))) {
    throw new Error("large Chromium receive unexpectedly used memory recovery");
  }
  return true;
}

function extractResumeEvidence(receiverLog, fileBytes) {
  const log = String(receiverLog || "");
  const found = log.match(/Found saved partial for .*?: (\d+)\/(\d+) bytes\./);
  const accepted = log.match(/Sender accepted .*? resume at (\d+) bytes\./);
  if (!found) throw new Error("receiver did not report a saved OPFS partial after refresh");
  if (!accepted) throw new Error("receiver did not report the accepted resume offset");
  const savedOffset = Number(found[1]);
  const savedSize = Number(found[2]);
  const acceptedOffset = Number(accepted[1]);
  if (savedSize !== fileBytes) {
    throw new Error(`saved partial size metadata mismatch: got ${savedSize}, want ${fileBytes}`);
  }
  if (savedOffset < RESUME_CHECKPOINT_BYTES || savedOffset >= fileBytes) {
    throw new Error(`saved partial offset is not a resumable checkpoint: ${savedOffset}`);
  }
  if (acceptedOffset !== savedOffset) {
    throw new Error(`sender accepted ${acceptedOffset} bytes, saved partial was ${savedOffset} bytes`);
  }
  return {
    checkpoint_bytes: RESUME_CHECKPOINT_BYTES,
    saved_partial_bytes: savedOffset,
    accepted_offset_bytes: acceptedOffset,
  };
}

async function pageTransferMetrics(page) {
  const metrics = await page.evaluate(() => window.__kigoLastTransferMetrics || null);
  return summarizeTransferMetrics(metrics);
}

async function withTimeout(label, timeoutMS, fn) {
  let timer;
  try {
    return await Promise.race([
      fn(),
      new Promise((_, reject) => {
        timer = setTimeout(() => reject(new Error(`${label} timed out after ${timeoutMS}ms`)), timeoutMS);
      }),
    ]);
  } finally {
    clearTimeout(timer);
  }
}

async function installRouteProbe(context, forceTurn) {
  await context.addInitScript(({ forceTurn }) => {
    const NativePeerConnection = window.RTCPeerConnection;
    const routeRecords = [];
    const peerConnections = [];
    Object.defineProperty(window, "__kigoPublicRouteRecords", { value: routeRecords });
    Object.defineProperty(window, "__kigoPublicPeerConnections", { value: peerConnections });
    window.RTCPeerConnection = new Proxy(NativePeerConnection, {
      construct(Target, args) {
        const config = { ...(args[0] || {}) };
        if (forceTurn) config.iceTransportPolicy = "relay";
        const pc = Reflect.construct(Target, [config, ...args.slice(1)]);
        peerConnections.push(pc);
        let captured = false;
        const capture = async () => {
          if (captured || !["connected", "completed"].includes(pc.iceConnectionState)) return;
          try {
            const stats = await pc.getStats();
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
            if (!pair) return;
            const local = stats.get(pair.localCandidateId);
            const remote = stats.get(pair.remoteCandidateId);
            routeRecords.push({
              local_candidate_type: local?.candidateType || "unknown",
              remote_candidate_type: remote?.candidateType || "unknown",
              protocol: local?.protocol || remote?.protocol || "unknown",
            });
            captured = true;
          } catch {}
        };
        pc.addEventListener("iceconnectionstatechange", capture);
        pc.addEventListener("connectionstatechange", capture);
        return pc;
      },
    });
  }, { forceTurn });
}

async function peerDiagnostics(...pages) {
  const diagnostics = [];
  for (const page of pages.filter(Boolean)) {
    const pageDiagnostics = await page.evaluate(async () => {
      const peers = window.__kigoPublicPeerConnections || [];
      return Promise.all(peers.map(async (pc) => {
        const candidate_counts = {};
        try {
          const stats = await pc.getStats();
          for (const stat of stats.values()) {
            if (stat.type !== "local-candidate" && stat.type !== "remote-candidate") continue;
            const key = `${stat.type}:${stat.candidateType || "unknown"}:${stat.protocol || "unknown"}`;
            candidate_counts[key] = (candidate_counts[key] || 0) + 1;
          }
        } catch {}
        return {
          signaling_state: pc.signalingState,
          ice_connection_state: pc.iceConnectionState,
          ice_gathering_state: pc.iceGatheringState,
          connection_state: pc.connectionState,
          local_description: pc.localDescription?.type || "",
          remote_description: pc.remoteDescription?.type || "",
          candidate_counts,
        };
      }));
    }).catch(() => []);
    diagnostics.push(...pageDiagnostics);
  }
  return diagnostics;
}

async function newPage(context, target) {
  const page = await context.newPage();
  const logs = [];
  page.on("pageerror", (err) => logs.push(`pageerror: ${err.message}`));
  page.on("console", (msg) => {
    if (["error", "warning"].includes(msg.type())) logs.push(`${msg.type()}: ${msg.text()}`);
  });
  await page.goto(target, { waitUntil: "domcontentloaded" });
  return { page, logs };
}

async function routeRecords(...pages) {
  const records = [];
  for (const page of pages) {
    const pageRecords = await page.evaluate(() => window.__kigoPublicRouteRecords || []).catch(() => []);
    records.push(...pageRecords);
  }
  return records;
}

function assertRelayProof(records, forceTurn) {
  if (!records.length) throw new Error("selected ICE candidate pair was not captured");
  if (forceTurn && !records.some((record) => record.local_candidate_type === "relay")) {
    throw new Error("forced TURN run did not select a local relay candidate");
  }
}

async function runText(browser, options) {
  const context = await browser.newContext({ ignoreHTTPSErrors: options.ignoreTLSErrors });
  await installRouteProbe(context, options.forceTurn);
  const code = randomCode();
  const payload = `public browser text ${Date.now()}`;
  let receiver;
  let sender;
  try {
    receiver = await newPage(context, `${options.url}/#c=${code}`);
    await receiver.page.evaluate(() => {
      const decode = window.decodeTransferChunk;
      window.decodeTransferChunk = async (...args) => {
        const data = await decode(...args);
        await new Promise((resolve) => setTimeout(resolve, 10));
        return data;
      };
    });
    sender = await newPage(context, `${options.url}/`);
    await sender.page.click('button[data-tab="text"]');
    await sender.page.fill("#textInput", payload);
    await sender.page.fill("#textCode", code);
    await sender.page.click("#sendText");
    await Promise.all([
      sender.page.waitForFunction(() => document.querySelector("#log")?.textContent.includes("Transfer complete."), null, { timeout: options.timeoutMS }),
      receiver.page.waitForFunction(() => document.querySelector("#log")?.textContent.includes("Transfer complete."), null, { timeout: options.timeoutMS }),
    ]);
    const received = await receiver.page.locator("#textOutput").textContent();
    if (received !== payload) throw new Error("received text did not match sent text");
    const records = await routeRecords(sender.page, receiver.page);
    assertRelayProof(records, options.forceTurn);
    const logs = [...sender.logs, ...receiver.logs];
    if (logs.length) throw new Error(`browser console errors: ${logs.join(" | ")}`);
    return { bytes: Buffer.byteLength(payload), checksum_match: true, selected_routes: records };
  } catch (err) {
    err.message = sanitize(err.message, code);
    err.peerDiagnostics = await peerDiagnostics(sender?.page, receiver?.page);
    err.browserLogs = [...(sender?.logs || []), ...(receiver?.logs || [])].map((line) => sanitize(line, code));
    throw err;
  } finally {
    await context.close();
  }
}

async function runFile(browser, options) {
  const context = await browser.newContext({ acceptDownloads: true, ignoreHTTPSErrors: options.ignoreTLSErrors });
  await installRouteProbe(context, options.forceTurn);
  const code = randomCode();
  const work = fs.mkdtempSync(path.join(os.tmpdir(), "kigo-public-browser-"));
  const source = options.filePath || path.join(work, "payload.bin");
  const received = path.join(work, "received.bin");
  if (!options.filePath) {
    const output = fs.createWriteStream(source);
    let remaining = options.fileBytes;
    while (remaining > 0) {
      const size = Math.min(remaining, 1024 * 1024);
      if (!output.write(crypto.randomBytes(size))) await new Promise((resolve) => output.once("drain", resolve));
      remaining -= size;
    }
    await new Promise((resolve, reject) => output.end((err) => err ? reject(err) : resolve()));
  }
  let receiver;
  let sender;
  try {
    receiver = await newPage(context, `${options.url}/#c=${code}`);
    sender = await newPage(context, `${options.url}/`);
    await sender.page.setInputFiles("#fileInput", source);
    await sender.page.fill("#fileCode", code);
    await sender.page.click("#sendFile");
    await Promise.all([
      sender.page.waitForFunction(() => document.querySelector("#log")?.textContent.includes("Transfer complete."), null, { timeout: options.timeoutMS }),
      receiver.page.waitForFunction(() => document.querySelector("#log")?.textContent.includes("Transfer complete."), null, { timeout: options.timeoutMS }),
    ]);
    const [senderMetrics, receiverMetrics, receiverLog] = await Promise.all([
      pageTransferMetrics(sender.page),
      pageTransferMetrics(receiver.page),
      receiver.page.locator("#log").textContent(),
    ]);
    const opfsLargeFileVerified = assertLargeFileStorage(options, receiverMetrics, receiverLog);
    const [download] = await Promise.all([
      receiver.page.waitForEvent("download", { timeout: options.timeoutMS }),
      receiver.page.locator("#downloads a").first().click(),
    ]);
    await download.saveAs(received);
    const checksumMatch = sha256(source) === sha256(received);
    if (!checksumMatch) throw new Error("received file checksum did not match source");
    const records = await routeRecords(sender.page, receiver.page);
    assertRelayProof(records, options.forceTurn);
    const logs = [...sender.logs, ...receiver.logs];
    if (logs.length) throw new Error(`browser console errors: ${logs.join(" | ")}`);
    return {
      bytes: fs.statSync(source).size,
      checksum_match: true,
      opfs_large_file_verified: opfsLargeFileVerified,
      transfer_metrics: { sender: senderMetrics, receiver: receiverMetrics },
      selected_routes: records,
    };
  } catch (err) {
    err.message = sanitize(err.message, code);
    err.peerDiagnostics = await peerDiagnostics(sender?.page, receiver?.page);
    err.browserLogs = [...(sender?.logs || []), ...(receiver?.logs || [])].map((line) => sanitize(line, code));
    throw err;
  } finally {
    await context.close();
    fs.rmSync(work, { recursive: true, force: true });
  }
}

async function runResume(browser, options) {
  const context = await browser.newContext({ acceptDownloads: true, ignoreHTTPSErrors: options.ignoreTLSErrors });
  await installRouteProbe(context, options.forceTurn);
  const code = randomCode();
  const work = fs.mkdtempSync(path.join(os.tmpdir(), "kigo-public-browser-resume-"));
  const source = path.join(work, "resume-payload.bin");
  const received = path.join(work, "resume-received.bin");
  fs.writeFileSync(source, crypto.randomBytes(options.fileBytes));
  let receiver;
  let sender;
  try {
    receiver = await newPage(context, `${options.url}/#c=${code}`);
    sender = await newPage(context, `${options.url}/`);
    await sender.page.setInputFiles("#fileInput", source);
    await sender.page.fill("#fileCode", code);
    await sender.page.click("#sendFile");

    await receiver.page.waitForFunction((checkpoint) => {
      const text = document.querySelector("#log")?.textContent || "";
      return [...text.matchAll(/offset=(\d+)/g)].some((match) => Number(match[1]) >= checkpoint - 64 * 1024);
    }, RESUME_CHECKPOINT_BYTES, { timeout: options.timeoutMS }).catch((err) => {
      throw new Error(`initial receive did not reach the OPFS checkpoint: ${err.message}`);
    });
    const persistedBeforeRefresh = await receiver.page.evaluate(async (checkpoint) => {
      const root = await navigator.storage.getDirectory();
      const directory = await root.getDirectoryHandle("kigo-receive-v1");
      let largest = 0;
      for await (const [name, handle] of directory.entries()) {
        if (handle.kind !== "file" || !name.endsWith(".part")) continue;
        largest = Math.max(largest, (await handle.getFile()).size);
      }
      if (largest < checkpoint) throw new Error(`OPFS partial had only ${largest} bytes before refresh`);
      return largest;
    }, RESUME_CHECKPOINT_BYTES);

    await receiver.page.reload({ waitUntil: "domcontentloaded" });
    await receiver.page.waitForFunction(() => {
      const text = document.querySelector("#log")?.textContent || "";
      return /Found saved partial for .*?: [1-9]\d*\/\d+ bytes\./.test(text)
        && /Sender accepted .*? resume at [1-9]\d* bytes\./.test(text);
    }, null, { timeout: options.timeoutMS }).catch(async (err) => {
      const [senderLog, receiverLog] = await Promise.all([
        sender.page.locator("#log").textContent().catch(() => ""),
        receiver.page.locator("#log").textContent().catch(() => ""),
      ]);
      throw new Error(`refresh did not negotiate a nonzero resume offset: ${err.message}\nsender=${senderLog}\nreceiver=${receiverLog}`);
    });
    await Promise.all([
      sender.page.waitForFunction(() => document.querySelector("#log")?.textContent.includes("Transfer complete."), null, { timeout: options.timeoutMS }),
      receiver.page.waitForFunction(() => document.querySelector("#log")?.textContent.includes("Transfer complete."), null, { timeout: options.timeoutMS }),
    ]).catch(async (err) => {
      const [senderLog, receiverLog] = await Promise.all([
        sender.page.locator("#log").textContent().catch(() => ""),
        receiver.page.locator("#log").textContent().catch(() => ""),
      ]);
      throw new Error(`resumed transfer did not complete: ${err.message}\nsender=${senderLog}\nreceiver=${receiverLog}`);
    });

    const [senderMetrics, receiverMetrics, receiverLog] = await Promise.all([
      pageTransferMetrics(sender.page),
      pageTransferMetrics(receiver.page),
      receiver.page.locator("#log").textContent(),
    ]);
    const resumeEvidence = extractResumeEvidence(receiverLog, options.fileBytes);
    if (resumeEvidence.saved_partial_bytes < persistedBeforeRefresh) {
      throw new Error(`saved partial shrank across refresh: before=${persistedBeforeRefresh}, resumed=${resumeEvidence.saved_partial_bytes}`);
    }
    const [download] = await Promise.all([
      receiver.page.waitForEvent("download", { timeout: options.timeoutMS }),
      receiver.page.locator("#downloads a").first().click(),
    ]);
    await download.saveAs(received);
    if (sha256(source) !== sha256(received)) throw new Error("resumed file checksum did not match source");
    const records = await routeRecords(sender.page, receiver.page);
    assertRelayProof(records, options.forceTurn);
    const logs = [...sender.logs, ...receiver.logs];
    if (logs.length) throw new Error(`browser console errors: ${logs.join(" | ")}`);
    return {
      bytes: fs.statSync(source).size,
      checksum_match: true,
      page_refreshes: 1,
      resume: resumeEvidence,
      transfer_metrics: { sender: senderMetrics, receiver: receiverMetrics },
      selected_routes: records,
    };
  } catch (err) {
    err.message = sanitize(err.message, code);
    err.peerDiagnostics = await peerDiagnostics(sender?.page, receiver?.page);
    err.browserLogs = [...(sender?.logs || []), ...(receiver?.logs || [])].map((line) => sanitize(line, code));
    throw err;
  } finally {
    await context.close();
    fs.rmSync(work, { recursive: true, force: true });
  }
}

async function main() {
  let options;
  try {
    options = validateOptions(parseArgs(process.argv.slice(2)));
  } catch (err) {
    console.error(err.message);
    process.exitCode = 2;
    return;
  }
  const config = {
    url: options.url,
    engine: options.engine,
    channel: options.channel || "bundled",
    force_turn: options.forceTurn,
    ignore_tls_errors: options.ignoreTLSErrors,
    scenarios: options.scenarios,
    timeout_ms: options.timeoutMS,
    artifact_dir: options.artifactDir,
  };
  if (options.dryRun) {
    console.log(JSON.stringify({ schema_version: 1, valid: true, ...config }, null, 2));
    return;
  }

  const playwright = require("playwright");
  const requestContext = await playwright.request.newContext({ ignoreHTTPSErrors: options.ignoreTLSErrors });
  let iceConfig;
  try {
    const iceResponse = await requestContext.get(`${options.url}/api/ice`);
    if (!iceResponse.ok()) throw new Error(`/api/ice returned ${iceResponse.status()}`);
    iceConfig = await iceResponse.json();
  } finally {
    await requestContext.dispose();
  }
  const servers = Array.isArray(iceConfig.iceServers) ? iceConfig.iceServers : [];
  const turnServers = servers.filter((server) => {
    const urls = Array.isArray(server.urls) ? server.urls : [server.urls];
    return urls.some((url) => /^turns?:/i.test(String(url || "")));
  });
  if (options.forceTurn && !turnServers.length) throw new Error("forced TURN run requires /api/ice to advertise TURN");

  const browserType = playwright[options.engine];
  const launchOptions = { headless: true };
  if (options.channel) launchOptions.channel = options.channel;
  const browser = await browserType.launch(launchOptions);
  const report = {
    schema_version: 1,
    kind: "public-browser-turn-matrix",
    generated_at: new Date().toISOString(),
    status: "passed",
    browser: {
      engine: options.engine,
      version: browser.version(),
      channel: options.channel || "bundled",
    },
    service_origin: new URL(options.url).origin,
    force_turn: options.forceTurn,
    tls_verification: options.ignoreTLSErrors ? "disabled_for_test" : "strict",
    turn: {
      advertised_servers: turnServers.length,
      authenticated: turnServers.length > 0 && turnServers.every((server) => Boolean(server.username && server.credential)),
    },
    scenarios: [],
  };
  try {
    for (const name of options.scenarios) {
      const started = Date.now();
      try {
        const result = name === "text"
          ? await runText(browser, options)
          : name === "resume"
            ? await runResume(browser, options)
            : await runFile(browser, options);
        report.scenarios.push({ name, status: "passed", duration_ms: Date.now() - started, ...result });
        console.log(`ok public browser ${name}`);
      } catch (err) {
        report.status = "failed";
        report.scenarios.push({
          name,
          status: "failed",
          duration_ms: Date.now() - started,
          reason: sanitize(err.message),
          peer_diagnostics: err.peerDiagnostics || [],
          browser_logs: err.browserLogs || [],
        });
        console.error(`failed public browser ${name}: ${sanitize(err.message)}`);
      }
    }
  } finally {
    await browser.close();
  }
  fs.mkdirSync(options.artifactDir, { recursive: true });
  const reportPath = path.join(options.artifactDir, "matrix.json");
  fs.writeFileSync(reportPath, JSON.stringify(report, null, 2) + "\n", { mode: 0o600 });
  console.log(`Public browser matrix artifact: ${reportPath}`);
  if (report.status !== "passed") process.exitCode = 1;
}

module.exports = {
  assertLargeFileStorage,
  assertRelayProof,
  extractResumeEvidence,
  parseArgs,
  sanitize,
  summarizeTransferMetrics,
  validateOptions,
};

if (require.main === module) {
  main().catch((err) => {
    console.error(sanitize(err.message));
    process.exitCode = 1;
  });
}
