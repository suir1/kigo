#!/usr/bin/env node

const crypto = require("crypto");
const fs = require("fs");
const os = require("os");
const path = require("path");

const ALPHABET = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789";

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
  if (!scenarios.length || scenarios.some((value) => !['text', 'file'].includes(value))) {
    throw new Error("scenarios must be a comma-separated subset of text,file");
  }
  const timeoutSeconds = Number(raw.timeout_seconds);
  if (!Number.isInteger(timeoutSeconds) || timeoutSeconds < 1 || timeoutSeconds > 600) {
    throw new Error("timeout seconds must be an integer between 1 and 600");
  }
  return {
    url: url.origin + url.pathname.replace(/\/$/, ""),
    engine: raw.engine,
    channel: raw.channel || "",
    forceTurn: raw.force_turn === '1',
    ignoreTLSErrors: raw.ignore_tls_errors === '1',
    scenarios,
    timeoutMS: timeoutSeconds * 1000,
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
  const source = path.join(work, "payload.bin");
  const received = path.join(work, "received.bin");
  fs.writeFileSync(source, crypto.randomBytes(256 * 1024));
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
    return { bytes: fs.statSync(source).size, checksum_match: true, selected_routes: records };
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
        const result = name === "text" ? await runText(browser, options) : await runFile(browser, options);
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

module.exports = { parseArgs, validateOptions, sanitize, assertRelayProof };

if (require.main === module) {
  main().catch((err) => {
    console.error(sanitize(err.message));
    process.exitCode = 1;
  });
}
