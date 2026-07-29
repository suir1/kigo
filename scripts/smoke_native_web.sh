#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
BIN=${KIGO_BIN:-}
if [[ -n "${KIGO_LISTEN:-}" ]]; then
  LISTEN=$KIGO_LISTEN
else
  LISTEN=""
  for port in $(seq 18080 18120); do
    if ! nc -z 127.0.0.1 "$port" >/dev/null 2>&1; then
      LISTEN="127.0.0.1:$port"
      break
    fi
  done
  if [[ -z "$LISTEN" ]]; then
    echo "No free localhost port found in 18080..18120." >&2
    exit 2
  fi
fi
BASE_URL=${KIGO_BASE_URL:-http://$LISTEN}
EXTERNAL_SERVICE=${KIGO_SMOKE_EXTERNAL_SERVICE:-0}
IGNORE_TLS_ERRORS=${KIGO_SMOKE_IGNORE_TLS_ERRORS:-0}
for value in "$EXTERNAL_SERVICE" "$IGNORE_TLS_ERRORS"; do
  if [[ "$value" != "0" && "$value" != "1" ]]; then
    echo "KIGO_SMOKE_EXTERNAL_SERVICE and KIGO_SMOKE_IGNORE_TLS_ERRORS must be 0 or 1" >&2
    exit 2
  fi
done
BROWSER=${PLAYWRIGHT_BROWSER:-chromium}
case "$BROWSER" in
  chromium|firefox|webkit) ;;
  *)
    echo "PLAYWRIGHT_BROWSER must be chromium, firefox, or webkit" >&2
    exit 2
    ;;
esac
if [[ -n "${PLAYWRIGHT_CHANNEL+x}" ]]; then
  CHANNEL=$PLAYWRIGHT_CHANNEL
elif [[ "$BROWSER" == "chromium" ]]; then
  CHANNEL=${PLAYWRIGHT_CHROMIUM_CHANNEL-chrome}
else
  CHANNEL=""
fi
TURN_SECRET=${KIGO_TURN_SECRET:-turn-smoke-secret}
TURN_LISTEN=${KIGO_TURN_LISTEN:-127.0.0.1:0}
TURN_PUBLIC_IP=${KIGO_TURN_PUBLIC_IP:-127.0.0.1}
TURN_ENABLED=${KIGO_SMOKE_TURN_ENABLED:-1}
if [[ "$TURN_ENABLED" != "0" && "$TURN_ENABLED" != "1" ]]; then
  echo "KIGO_SMOKE_TURN_ENABLED must be 0 or 1" >&2
  exit 2
fi

WORK=$(mktemp -d)
SERVER_PID=""
HISTORY="$WORK/route-history.json"
export KIGO_NOTE_DRAFT_PATH="$WORK/note-drafts"
export KIGO_CONFIG_PATH="$WORK/config.json"

cleanup() {
  if [[ -n "$SERVER_PID" ]]; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

if [[ -z "$BIN" ]]; then
  BIN="$WORK/kigo"
  (cd "$ROOT" && go build -o "$BIN" ./cmd/kigo)
fi

if ! node -e "require.resolve('playwright')" >/dev/null 2>&1; then
  if [[ -d /Users/sui/Code/openclaw/node_modules ]]; then
    export NODE_PATH=/Users/sui/Code/openclaw/node_modules${NODE_PATH:+:$NODE_PATH}
  fi
fi

if ! node -e "require.resolve('playwright')" >/dev/null 2>&1; then
  echo "Playwright is required for this smoke test." >&2
  echo "Install it in this repo or export NODE_PATH to a node_modules directory containing playwright." >&2
  exit 2
fi

if [[ "$EXTERNAL_SERVICE" == "0" ]]; then
  serve_args=(serve --listen "$LISTEN" \
    --signal-requests-per-minute 600 \
    --native-relay 127.0.0.1:9)
  if [[ "$TURN_ENABLED" == "1" ]]; then
    serve_args+=(
      --turn-listen "$TURN_LISTEN"
      --turn-public-ip "$TURN_PUBLIC_IP"
      --turn-secret "$TURN_SECRET"
      --turn-credential-ttl 5m
      --turn-max-allocations 64
      --turn-max-allocations-per-user 4
      --turn-max-allocations-per-ip 16
      --turn-egress-window 1h
      --turn-max-egress-mib 1024
      --turn-max-egress-mib-per-user 512
      --turn-max-egress-mib-per-ip 768
    )
  fi
  "$BIN" "${serve_args[@]}" \
    >"$WORK/service.log" 2>"$WORK/service.err" &
  SERVER_PID=$!
fi
curl_args=(-fsS)
if [[ "$IGNORE_TLS_ERRORS" == "1" ]]; then
  curl_args+=(-k)
fi
for _ in $(seq 1 80); do
  if curl "${curl_args[@]}" "$BASE_URL/api/ice" >/dev/null 2>&1; then
    break
  fi
  sleep 0.1
done
curl "${curl_args[@]}" "$BASE_URL/api/ice" >"$WORK/ice.json"
grep -q 'stun:stun1.l.google.com:19302' "$WORK/ice.json"
curl "${curl_args[@]}" "$BASE_URL/api/health" >"$WORK/health.json"
grep -q '"ok":true' "$WORK/health.json"
if [[ "$EXTERNAL_SERVICE" == "0" ]]; then
  grep -q '"endpoint":"127.0.0.1:9"' "$WORK/health.json"
fi
if [[ "$TURN_ENABLED" == "1" ]]; then
  grep -Eq '"username":"[0-9]+:kigo-[0-9a-f]+"' "$WORK/ice.json"
  grep -q '"credentialExpiresAt":' "$WORK/ice.json"
  grep -q '"credential_mode":"temporary"' "$WORK/health.json"
  if [[ "$EXTERNAL_SERVICE" == "0" ]]; then
    grep -q '"max_egress_bytes":1073741824' "$WORK/health.json"
    grep -q '"max_egress_bytes_per_user":536870912' "$WORK/health.json"
    grep -q '"max_egress_bytes_per_ip":805306368' "$WORK/health.json"
  fi
  grep -q '"egress_bytes_total":' "$WORK/health.json"
fi

echo "service: $BASE_URL"
"$BIN" --route-history "$HISTORY" --signal "$BASE_URL" doctor --timeout 2s >/dev/null
"$BIN" --route-history "$HISTORY" --signal "$BASE_URL" route --json --pair web-web --timeout 2s >"$WORK/route.json"
grep -q '"pair": "web-web"' "$WORK/route.json"
grep -q '"scope": {' "$WORK/route.json"
grep -q '"probe": {' "$WORK/route.json"
grep -q '"latency_ms":' "$WORK/route.json"
grep -q '"stun_probe": {' "$WORK/route.json"
grep -q '"class":' "$WORK/route.json"
echo "ok doctor signaling"

node - "$BIN" "$BASE_URL" "$WORK" "$BROWSER" "$CHANNEL" "$HISTORY" <<'NODE'
const { chromium, firefox, webkit } = require("playwright");
const { spawn } = require("child_process");
const crypto = require("crypto");
const fs = require("fs");
const os = require("os");
const path = require("path");
const zlib = require("zlib");

const [bin, baseURL, work, browserName, channel, routeHistory] = process.argv.slice(2);
const nativeInterface = process.env.KIGO_SMOKE_NATIVE_INTERFACE || "";
const ignoreTLSErrors = process.env.KIGO_SMOKE_IGNORE_TLS_ERRORS === "1";
const browserArgs = (process.env.KIGO_SMOKE_BROWSER_ARGS || "")
  .split(",")
  .map((value) => value.trim())
  .filter(Boolean);
const smokeFilters = (process.env.KIGO_SMOKE_FILTER || "")
  .split(",")
  .map((value) => value.trim())
  .filter(Boolean);

function kigoArgs(args) {
  const interfaceArgs = nativeInterface ? ["--interface", nativeInterface] : [];
  return ["--route-history", routeHistory, ...interfaceArgs, ...args];
}

function writeFile(file, text) {
  fs.mkdirSync(path.dirname(file), { recursive: true });
  fs.writeFileSync(file, text);
}

function sha256(file) {
  return crypto.createHash("sha256").update(fs.readFileSync(file)).digest("hex");
}

function sampleSha256(file) {
  const data = fs.readFileSync(file);
  const chunkSize = 128 * 1024;
  const sampleWindow = 1024 * 1024;
  if (data.length <= chunkSize * 2) return crypto.createHash("sha256").update(data).digest("hex");
  const hash = crypto.createHash("sha256");
  hash.update(data.subarray(0, chunkSize));
  hash.update(data.subarray(data.length - chunkSize));
  let sampled = chunkSize * 2;
  const windows = Math.max(1, Math.floor((data.length - chunkSize) / sampleWindow));
  for (let window = 0; window < windows && sampled < sampleWindow; window++) {
    const offset = window * sampleWindow + sampleWindow / 2;
    if (offset + chunkSize > data.length) break;
    hash.update(data.subarray(offset, offset + chunkSize));
    sampled += chunkSize;
  }
  return hash.digest("hex");
}

function randomPairingCode() {
  const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789";
  return Array.from(crypto.randomBytes(6), (byte) => alphabet[byte % alphabet.length]).join("");
}

function assertEqual(actual, expected, message) {
  if (actual !== expected) throw new Error(`${message}: got ${actual}, want ${expected}`);
}

function waitProc(proc) {
  if (proc.exitCode !== null) return Promise.resolve(proc.exitCode);
  return new Promise((resolve) => proc.on("close", (code) => resolve(code)));
}

async function waitForOutput(output, pattern, label, ms = 5000) {
  const deadline = Date.now() + ms;
  while (Date.now() < deadline) {
    const { stdout, stderr } = output();
    if (pattern.test(stdout) || pattern.test(stderr)) return;
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  const { stdout, stderr } = output();
  throw new Error(`${label} did not appear in process output\nstdout=${stdout}\nstderr=${stderr}`);
}

async function waitForPageCompleteOrProcessExit(page, proc, output, label) {
  const complete = waitForTransferComplete(page)
    .then(() => ({ kind: "page" }))
    .catch((err) => ({ kind: "page-error", err }));
  const exited = waitProc(proc).then((code) => ({ kind: "process", code }));
  const first = await Promise.race([complete, exited]);
  if (first.kind === "page") return null;
  if (first.kind === "page-error") throw first.err;
  try {
    await waitForTransferComplete(page, 3000);
    return first.code;
  } catch (err) {
    const { stdout, stderr } = output();
    const log = await page.locator("#log").textContent({ timeout: 500 }).catch(() => "");
    throw new Error(`${label} receiver exited before web page completed, code=${first.code}\npage log=${log}\nstdout=${stdout}\nstderr=${stderr}`);
  }
}

async function withTimeout(label, ms, fn) {
  let timer;
  try {
    return await Promise.race([
      fn(),
      new Promise((_, reject) => {
        timer = setTimeout(() => reject(new Error(`${label} timed out after ${ms}ms`)), ms);
      }),
    ]);
  } finally {
    clearTimeout(timer);
  }
}

async function runSmoke(browser, label, ms, fn) {
  if (smokeFilters.length && !smokeFilters.some((filter) => label.includes(filter))) return;
  const context = await browser.newContext({ acceptDownloads: true, ignoreHTTPSErrors: ignoreTLSErrors });
  await context.addInitScript(() => {
    if (window.__kigoSmokePeerConnections) return;
    const NativePeerConnection = window.RTCPeerConnection;
    const peers = [];
    Object.defineProperty(window, "__kigoSmokePeerConnections", { value: peers });
    window.RTCPeerConnection = new Proxy(NativePeerConnection, {
      construct(Target, args) {
        const peer = Reflect.construct(Target, args);
        peers.push(peer);
        return peer;
      },
    });
  });
  try {
    await withTimeout(label, ms, () => fn(context));
  } catch (err) {
    const pages = context.pages();
    for (let i = 0; i < pages.length; i++) {
      const page = pages[i];
      let url = "";
      let log = "";
      try {
        url = page.url();
        log = await page.locator("#log").textContent({ timeout: 500 });
      } catch {}
      const peers = await peerConnectionDiagnostics(page);
      console.error(`context dump ${label} page ${i}: ${url}\n${log}\npeer connections: ${JSON.stringify(peers)}`);
    }
    throw err;
  } finally {
    await context.close();
  }
}

async function peerConnectionDiagnostics(page) {
  return page.evaluate(async () => {
    const summarizeSDP = (sdp) => String(sdp)
      .split(/\r?\n/)
      .filter((line) => /^(m=application|a=(setup|sctp-port|max-message-size|ice-options):)/.test(line))
      .join(" | ");
    const peers = window.__kigoSmokePeerConnections || [];
    return Promise.all(peers.map(async (pc) => {
      const candidateCounts = {};
      const candidatePairs = [];
      try {
        const stats = await pc.getStats();
        for (const stat of stats.values()) {
          if (stat.type === "candidate-pair") {
            candidatePairs.push({ state: stat.state, nominated: stat.nominated });
          }
          if (stat.type === "local-candidate" || stat.type === "remote-candidate") {
            const key = `${stat.type}:${stat.candidateType}:${stat.protocol}`;
            candidateCounts[key] = (candidateCounts[key] || 0) + 1;
          }
        }
      } catch {}
      return {
        signalingState: pc.signalingState,
        iceConnectionState: pc.iceConnectionState,
        iceGatheringState: pc.iceGatheringState,
        connectionState: pc.connectionState,
        localDescription: pc.localDescription?.type || "",
        remoteDescription: pc.remoteDescription?.type || "",
        localSDP: summarizeSDP(pc.localDescription?.sdp || ""),
        remoteSDP: summarizeSDP(pc.remoteDescription?.sdp || ""),
        candidateCounts,
        candidatePairs,
      };
    }));
  }).catch(() => []);
}

async function closeSmokeBrowser(browser) {
  for (const context of browser.contexts()) {
    await withTimeout("browser context close", 5000, () => context.close()).catch((err) => {
      console.warn(`warning: ${err.message}`);
    });
  }
  await withTimeout("browser close", 10000, () => browser.close()).catch((err) => {
    console.warn(`warning: ${err.message}; transfer assertions already completed`);
  });
}

function trackPage(page) {
  const logs = [];
  page.on("pageerror", (err) => logs.push(`pageerror: ${err.message}`));
  page.on("console", (msg) => {
    if (["error", "warning"].includes(msg.type())) logs.push(`${msg.type()}: ${msg.text()}`);
  });
  return { page, logs };
}

function waitForTransferComplete(page, timeout = 30000) {
  return page.waitForFunction(() => {
    const log = document.querySelector("#log")?.textContent || "";
    return log.includes("Transfer complete.");
  }, null, { timeout });
}

async function saveDownload(page, trigger, destination, timeout = 10000) {
  const [download] = await Promise.all([
    page.waitForEvent("download", { timeout }),
    trigger(),
  ]);
  await download.saveAs(destination);
  return download;
}

async function newPage(context, target = baseURL, options = {}) {
  const page = await context.newPage();
  const tracked = trackPage(page);
  if (options.beforeGoto) await options.beforeGoto(page);
  const automaticDownload = options.watchDownload
    ? page.waitForEvent("download", { timeout: options.downloadTimeout || 30000 }).catch(() => null)
    : null;
  await page.goto(target);
  const done = options.waitForCompletion
    ? waitForTransferComplete(page, options.completionTimeout || 30000)
    : null;
  if (done) done.catch(() => {});
  return { ...tracked, automaticDownload, done };
}

async function extractCode(page) {
  return await page.waitForFunction(() => {
    const text = document.querySelector("#share")?.textContent || "";
    return text.match(/Code:\s*([A-Z2-9]{6})/)?.[1] || "";
  }, null, { timeout: 5000 }).then((handle) => handle.jsonValue());
}

async function startWebReceiver(context, code, search = "", options = {}) {
  return newPage(context, `${baseURL}/${search}#c=${code}`, {
    ...options,
    waitForCompletion: true,
  });
}

function spawnKigo(args, { stdin = false } = {}) {
  const proc = spawn(bin, kigoArgs(args), { stdio: [stdin ? "pipe" : "ignore", "pipe", "pipe"] });
  let stdout = "";
  let stderr = "";
  proc.stdout.on("data", (data) => { stdout += data; });
  proc.stderr.on("data", (data) => { stderr += data; });
  return { proc, output: () => ({ stdout, stderr }) };
}

function spawnRecv(code, out, extraArgs = []) {
  return spawnKigo(["recv", code, "--signal", baseURL, "--out", out, ...extraArgs]);
}

function spawnTextRecv(code) {
  return spawnKigo(["text", "recv", code, "--signal", baseURL]);
}

function spawnFileSend(file, extraArgs = []) {
  return spawnKigo(["send", file, "--signal", baseURL, ...extraArgs]);
}

function spawnNote(args) {
  return spawnKigo(["--signal", baseURL, "--web-url", baseURL, "note", ...args], { stdin: true });
}

async function stopProc(proc) {
  if (!proc || proc.exitCode !== null) return;
  proc.kill("SIGTERM");
  try {
    await withTimeout("native process stop", 3000, () => waitProc(proc));
  } catch {
    proc.kill("SIGKILL");
    await waitProc(proc);
  }
}

async function waitForPairingCode(sender, label) {
  await waitForOutput(sender.output, /^Code:\s*[A-Z2-9]{6}/m, `${label} pairing code`, 10000);
  const { stdout, stderr } = sender.output();
  const code = stdout.match(/^Code:\s*([A-Z2-9]{6})/m)?.[1] || "";
  if (!code) throw new Error(`${label} sender did not emit a code\nstdout=${stdout}\nstderr=${stderr}`);
  return code;
}

async function transferWebInputToNative(context, {
  src,
  out,
  label,
  receiverArgs = [],
  inputSelector = "#fileInput",
  sendSelector = "#sendFile",
}) {
  const code = randomPairingCode();
  const recv = spawnRecv(code, out, receiverArgs);
  await waitForOutput(recv.output, /Joining room:/, `${label} receiver startup`);

  const tracked = await newPage(context);
  await tracked.page.evaluate((fixedCode) => { window.generateCode = () => fixedCode; }, code);
  await tracked.page.setInputFiles(inputSelector, src);
  if (sendSelector) await tracked.page.click(sendSelector);
  let exitCode = await waitForPageCompleteOrProcessExit(tracked.page, recv.proc, recv.output, label);
  if (exitCode === null) exitCode = await waitProc(recv.proc);
  const output = recv.output();
  assertEqual(exitCode, 0, `${label} native receiver exited non-zero\nstdout=${output.stdout}\nstderr=${output.stderr}`);
  return {
    ...tracked,
    ...output,
    pageLog: await tracked.page.locator("#log").textContent(),
  };
}

async function nativeToWebFile(browser) {
  console.log("start native->web file");
  const dir = path.join(work, "native-to-web-file");
  const src = path.join(dir, "native-to-web.txt");
  const dst = path.join(dir, "downloaded.txt");
  writeFile(src, (`native compressible payload ${Date.now()}\n`).repeat(12000));

  const send = spawnFileSend(src);
  console.log("  waiting for native sender code");
  const code = await waitForPairingCode(send, "native file sender");
  console.log(`  code ${code}`);

  let received;
  try {
    received = await startWebReceiver(browser, code, "", { watchDownload: true });
    await received.done;
  } catch (err) {
    const { stdout, stderr } = send.output();
    throw new Error(`${err.message}\nnative sender exit=${send.proc.exitCode}\nstdout=${stdout}\nstderr=${stderr}`);
  }
  const { page, logs } = received;
  console.log("  web receiver completed transfer");
  const pageLog = await page.locator("#log").textContent();
  if (!pageLog.includes("Path: ")) throw new Error(`native->web page did not report selected path\n${pageLog}`);
  const pathLine = pageLog.split("\n").find((line) => line.includes("Path: "))?.trim() || "";
  console.log(`  ${pathLine}`);
  const download = await received.automaticDownload;
  if (!download) throw new Error(`native->web automatic download did not start\n${pageLog}`);
  console.log("  download received");
  await download.saveAs(dst);
  const sendCode = await waitProc(send.proc);
  const { stdout, stderr } = send.output();
  console.log(`  native sender exited ${sendCode}`);
  assertEqual(sendCode, 0, `native send exited non-zero\nstdout=${stdout}\nstderr=${stderr}`);
  assertEqual(sha256(dst), sha256(src), "native->web file hash mismatch");
  if (!stdout.includes("compressed ")) throw new Error(`native sender did not report compressed chunks\nstdout=${stdout}`);
  if (logs.length) throw new Error(`native->web browser logs:\n${logs.join("\n")}`);
  console.log("ok native->web file");
}

async function browserPersistentReceiveResume(browser) {
  console.log("start browser same-code refresh resume");
  const dir = path.join(work, "browser-persistent-resume");
  const src = path.join(dir, "persistent-resume.bin");
  const dst = path.join(dir, "downloaded.bin");
  fs.mkdirSync(dir, { recursive: true });
  fs.writeFileSync(src, crypto.randomBytes(12 * 1024 * 1024));
  const item = {
    name: path.basename(src),
    size: fs.statSync(src).size,
    sha256: sha256(src),
    sample_sha256: sampleSha256(src),
  };

  const sender = spawnFileSend(src);
  const code = await waitForPairingCode(sender, "same-code persistent resume");
  const { page, logs } = await newPage(browser);
  await page.evaluate(() => {
    const decode = window.decodeTransferChunk;
    window.decodeTransferChunk = async (...args) => {
      const data = await decode(...args);
      await new Promise((resolve) => setTimeout(resolve, 20));
      return data;
    };
  });
  await page.click('[data-tab="receive"]');
  await page.fill("#codeInput", code);
  await page.click("#receiveButton");
  await page.waitForFunction((checkpoint) => {
    const text = document.querySelector("#log")?.textContent || "";
    return [...text.matchAll(/offset=(\d+)/g)].some((match) => Number(match[1]) >= checkpoint - 64 * 1024);
  }, 4 * 1024 * 1024, { timeout: 30000 });

  await page.reload();
  const wrongTokenError = await page.evaluate(async (pairingCode) => {
    const token = await roomToken(pairingCode);
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    const url = new URL(`${proto}//${location.host}/api/signal/${token}`);
    url.searchParams.set("role", "receiver");
    return await new Promise((resolve, reject) => {
      const ws = new WebSocket(url, ["kigo-reconnect-v1"]);
      const timer = setTimeout(() => reject(new Error("wrong reconnect token was not rejected")), 5000);
      ws.onopen = () => ws.send(JSON.stringify({
        type: "signal_join",
        reconnect_token: "wrong-reconnect-token",
      }));
      ws.onmessage = (event) => {
        const message = JSON.parse(event.data);
        if (message.type !== "error") return;
        clearTimeout(timer);
        ws.close();
        resolve(message.error || "");
      };
      ws.onerror = () => {
        clearTimeout(timer);
        reject(new Error("wrong reconnect token WebSocket failed before signaling error"));
      };
    });
  }, code);
  if (!wrongTokenError.includes("invalid reconnect token")) {
    throw new Error(`wrong reconnect token error mismatch: ${wrongTokenError}`);
  }

  const partialBefore = await page.evaluate(async (entry) => {
    const key = await browserPartialKey(entry);
    const root = await navigator.storage.getDirectory();
    const directory = await root.getDirectoryHandle(OPFS_RECEIVE_DIR);
    const handle = await directory.getFileHandle(`${key}.part`);
    const file = await handle.getFile();
    return { key, size: file.size };
  }, item);
  if (partialBefore.size < 4 * 1024 * 1024 || partialBefore.size >= item.size) {
    throw new Error(`unexpected persisted partial size ${partialBefore.size}`);
  }

  await page.evaluate((pairingCode) => {
    history.replaceState(null, "", `#c=${pairingCode}`);
  }, code);
  await page.reload();
  await waitForTransferComplete(page, 45000);
  await saveDownload(page, () => page.locator("#downloads a").first().click(), dst);
  const exitCode = await waitProc(sender.proc);
  const output = sender.output();
  assertEqual(exitCode, 0, `same-code resumed native sender exited non-zero\nstdout=${output.stdout}\nstderr=${output.stderr}`);
  if (!output.stdout.includes("reconnecting attempt 2/3")) {
    throw new Error(`native sender did not attempt same-code WebRTC reconnect\nstdout=${output.stdout}\nstderr=${output.stderr}`);
  }
  if (!/resuming persistent-resume\.bin from [1-9]\d*\/12582912 bytes/.test(output.stdout)) {
    throw new Error(`native sender did not resume browser OPFS partial\nstdout=${output.stdout}\nstderr=${output.stderr}`);
  }
  assertEqual(sha256(dst), item.sha256, "browser persistent resume download hash mismatch");
  const cacheAfter = await page.evaluate(async ({ key }) => {
    const root = await navigator.storage.getDirectory();
    const directory = await root.getDirectoryHandle(OPFS_RECEIVE_DIR);
    const handle = await directory.getFileHandle(`${key}.part`);
    return (await handle.getFile()).size;
  }, partialBefore);
  assertEqual(cacheAfter, item.size, "completed OPFS cache size mismatch");
  if (logs.length) throw new Error(`browser persistent resume logs:\n${logs.join("\n")}`);
  console.log("ok browser same-code refresh resume");
}

async function browserCorruptPersistentResume(browser) {
  console.log("start browser corrupt persistent resume");
  const dir = path.join(work, "browser-corrupt-persistent-resume");
  const src = path.join(dir, "persistent-corrupt.bin");
  const dst = path.join(dir, "downloaded.bin");
  fs.mkdirSync(dir, { recursive: true });
  fs.writeFileSync(src, crypto.randomBytes(1024 * 1024));
  const item = {
    name: path.basename(src),
    size: fs.statSync(src).size,
    sha256: sha256(src),
    sample_sha256: sampleSha256(src),
  };
  const { page, logs } = await newPage(browser);
  await page.evaluate(async (entry) => {
    const key = await browserPartialKey(entry);
    const root = await navigator.storage.getDirectory();
    const directory = await root.getDirectoryHandle(OPFS_RECEIVE_DIR, { create: true });
    const handle = await directory.getFileHandle(`${key}.part`, { create: true });
    const writer = await handle.createWritable();
    await writer.write(new Uint8Array(120000).fill(0x78));
    await writer.close();
  }, item);

  const sender = spawnFileSend(src);
  const code = await waitForPairingCode(sender, "corrupt persistent resume");
  await page.click('[data-tab="receive"]');
  await page.fill("#codeInput", code);
  await page.click("#receiveButton");
  await waitForTransferComplete(page, 45000);
  const pageLog = await page.locator("#log").textContent();
  await saveDownload(page, () => page.locator("#downloads a").first().click(), dst);
  const exitCode = await waitProc(sender.proc);
  const output = sender.output();
  assertEqual(exitCode, 0, `corrupt persistent sender exited non-zero\nstdout=${output.stdout}\nstderr=${output.stderr}`);
  if (!output.stdout.includes("resume prefix mismatch for persistent-corrupt.bin; restarting from 0")) {
    throw new Error(`native sender did not reject corrupt browser prefix\nstdout=${output.stdout}\nstderr=${output.stderr}`);
  }
  if (!pageLog.includes("Sender accepted persistent-corrupt.bin resume at 0 bytes.")) {
    throw new Error(`browser did not truncate rejected OPFS prefix\n${pageLog}`);
  }
  assertEqual(sha256(dst), item.sha256, "browser corrupt persistent resume download hash mismatch");
  if (logs.length) throw new Error(`browser corrupt persistent resume logs:\n${logs.join("\n")}`);
  console.log("ok browser corrupt persistent resume");
}

async function nativeToWebText(browser) {
  console.log("start native->web text");
  const payload = `native text payload ${Date.now()}`;
  const code = "NATIVE-WEB-TEXT-CUSTOM-2026";
  const send = spawnKigo(["text", "send", payload, "--signal", baseURL, "--code", code.toLowerCase()]);
  await waitForOutput(send.output, new RegExp(`^Code:\\s*${code}$`, "m"), "native text sender normalized custom code", 10000);

  const { page, logs, done } = await startWebReceiver(browser, code.toLowerCase());
  await done;
  const received = await page.locator("#textOutput").textContent();
  const sendCode = await waitProc(send.proc);
  const { stdout, stderr } = send.output();
  assertEqual(sendCode, 0, `native text send exited non-zero\nstdout=${stdout}\nstderr=${stderr}`);
  assertEqual(received, payload, "native->web text mismatch");
  if (logs.length) throw new Error(`native-text->web browser logs:\n${logs.join("\n")}`);
  console.log("ok native->web text");
}

async function webToNativeFile(browser) {
  console.log("start web->native file");
  const dir = path.join(work, "web-to-native-file");
  const src = path.join(dir, "web-to-native.txt");
  const out = path.join(dir, "out");
  const saved = path.join(out, "web-to-native.txt");
  writeFile(src, (`web compressible payload ${Date.now()}\n`).repeat(12000));
  fs.mkdirSync(out, { recursive: true });
  const { logs, stdout, pageLog } = await transferWebInputToNative(browser, {
    src, out, label: "web->native file",
  });
  assertEqual(sha256(saved), sha256(src), "web->native file hash mismatch");
  if (!pageLog.includes("Compressed ")) throw new Error(`web sender did not report compressed chunks\n${pageLog}`);
  if (!stdout.includes("compression gzip negotiated")) throw new Error(`native receiver did not negotiate gzip\nstdout=${stdout}`);
  if (logs.length) throw new Error(`web->native browser logs:\n${logs.join("\n")}`);
  console.log("ok web->native file");
}

async function webToNativeSkipFile(browser) {
  console.log("start web->native skip file");
  const dir = path.join(work, "web-to-native-skip-file");
  const src = path.join(dir, "web-to-native-skip.txt");
  const out = path.join(dir, "out");
  const saved = path.join(out, "web-to-native-skip.txt");
  writeFile(src, `new web payload ${Date.now()}\n`);
  fs.mkdirSync(out, { recursive: true });
  writeFile(saved, "keep existing payload\n");
  const before = sha256(saved);
  const { logs, stdout } = await transferWebInputToNative(browser, {
    src, out, label: "web->native skip file", receiverArgs: ["--on-conflict", "skip"],
  });
  assertEqual(sha256(saved), before, "web->native skip changed existing file");
  if (!stdout.includes("skipping existing")) throw new Error(`native skip decision was not logged\nstdout=${stdout}`);
  if (logs.length) throw new Error(`web-skip->native browser logs:\n${logs.join("\n")}`);
  console.log("ok web->native skip file");
}

async function webToNativeCompletedFile(browser) {
  console.log("start web->native completed file");
  const dir = path.join(work, "web-to-native-completed-file");
  const src = path.join(dir, "web-to-native-completed.bin");
  const out = path.join(dir, "out");
  const saved = path.join(out, path.basename(src));
  fs.mkdirSync(out, { recursive: true });
  fs.writeFileSync(src, crypto.randomBytes(3 * 1024 * 1024));
  fs.copyFileSync(src, saved);
  const { logs, stdout, pageLog } = await transferWebInputToNative(browser, {
    src, out, label: "web->native completed file",
  });
  assertEqual(sha256(saved), sha256(src), "web->native completed skip changed file");
  if (!stdout.includes("already complete")) throw new Error(`native completed decision was not logged\nstdout=${stdout}`);
  if (!pageLog.includes("Skipped already-complete")) throw new Error(`web sender did not confirm completed skip\n${pageLog}`);
  if (logs.length) throw new Error(`web-completed->native browser logs:\n${logs.join("\n")}`);
  console.log("ok web->native completed file");
}

async function webToNativeText(browser) {
  console.log("start web->native text");
  const payload = `web text payload ${Date.now()}`;
  const code = "WEB-TEXT-CUSTOM-2026";
  const recv = spawnTextRecv(code);
  await waitForOutput(recv.output, /Joining room:/, "native text receiver startup");
  const { page, logs } = await newPage(browser);
  await page.click('button[data-tab="text"]');
  await page.fill("#textInput", payload);
  await page.fill("#textCode", code.toLowerCase());
  await page.click("#sendText");
  let recvCode = await waitForPageCompleteOrProcessExit(page, recv.proc, recv.output, "web->native text");
  if (recvCode === null) recvCode = await waitProc(recv.proc);
  const { stdout, stderr } = recv.output();
  assertEqual(recvCode, 0, `native text recv exited non-zero\nstdout=${stdout}\nstderr=${stderr}`);
  if (!stdout.split(/\r?\n/).includes(payload)) {
    throw new Error(`native text receiver output did not contain payload\nstdout=${stdout}\nstderr=${stderr}`);
  }
  if (logs.length) throw new Error(`web-text->native browser logs:\n${logs.join("\n")}`);
  console.log("ok web->native text");
}

async function webToWebFile(browser) {
  console.log("start web->web file");
  const parallelDisabled = process.env.KIGO_SMOKE_DISABLE_PARALLEL === "1";
  const forceRelay = process.env.KIGO_SMOKE_FORCE_RELAY === "1";
  const directICEOnly = process.env.KIGO_SMOKE_DIRECT_ICE_ONLY === "1";
  if (directICEOnly) {
    await browser.route("**/api/ice", async (route) => {
      const response = await route.fetch();
      const config = await response.json();
      const urls = (config.iceServers || [])
        .flatMap((server) => Array.isArray(server.urls) ? server.urls : [server.urls])
        .filter((url) => /^stun:stun(?:1)?\.l\.google\.com:19302$/i.test(String(url || "")));
      await route.fulfill({
        response,
        contentType: "application/json",
        body: JSON.stringify({ iceServers: [{ urls }] }),
      });
    });
  }
  if (forceRelay) {
    await browser.addInitScript(() => {
      const NativePeerConnection = window.RTCPeerConnection;
      window.RTCPeerConnection = new Proxy(NativePeerConnection, {
        construct(Target, args) {
          args[0] = { ...(args[0] || {}), iceTransportPolicy: "relay" };
          return Reflect.construct(Target, args);
        },
      });
    });
  }
  if (parallelDisabled) {
    await browser.route("**/app.js", async (route) => {
      const response = await route.fetch();
      const body = (await response.text()).replace(
        'const parallelData = negotiation?.features?.includes(FEATURE_PARALLEL_DATA) === true;',
        "const parallelData = false;",
      );
      await route.fulfill({ response, body });
    });
  }
  const dir = path.join(work, "web-to-web-file");
  const src = path.join(dir, "web-to-web.txt");
  const dst = path.join(dir, "downloaded.txt");
  const benchmarkBytes = Number(process.env.KIGO_SMOKE_WEB_WEB_BYTES || 0);
  if (benchmarkBytes > 0) {
    if (!Number.isSafeInteger(benchmarkBytes) || benchmarkBytes > 512 * 1024 * 1024) {
      throw new Error("KIGO_SMOKE_WEB_WEB_BYTES must be an integer between 1 and 536870912");
    }
    fs.mkdirSync(dir, { recursive: true });
    fs.writeFileSync(src, crypto.randomBytes(benchmarkBytes));
  } else {
    writeFile(src, `web to web payload ${Date.now()}\nsecond line\n`);
  }
  const started = Date.now();
  const code = randomPairingCode();
  const parallelSearch = parallelDisabled ? "" : "?parallel=1";
  const receiver = await startWebReceiver(browser, code, parallelSearch);

  const sender = await newPage(browser, `${baseURL}/${parallelSearch}`);
  await sender.page.evaluate((fixedCode) => { window.generateCode = () => fixedCode; }, code);
  await sender.page.setInputFiles("#fileInput", src);
  await sender.page.click("#sendFile");
  await waitForTransferComplete(sender.page);
  await receiver.done;

  await saveDownload(receiver.page, () => receiver.page.locator("#downloads a").first().click(), dst);
  assertEqual(sha256(dst), sha256(src), "web->web file hash mismatch");
  const senderLog = await sender.page.locator("#log").textContent();
  const receiverLog = await receiver.page.locator("#log").textContent();
  if (senderLog.includes("Reliable unordered file channel negotiated.") ||
      receiverLog.includes("Reliable unordered file channel negotiated.")) {
    throw new Error(`default web-web file unexpectedly enabled unordered data channel\nsender=${senderLog}\nreceiver=${receiverLog}`);
  }
  if (!parallelDisabled && (!senderLog.includes("Parallel WebRTC: 2 data paths.") ||
      !receiverLog.includes("Parallel WebRTC: 2 data paths."))) {
    throw new Error(`web-web file did not negotiate parallel data paths\nsender=${senderLog}\nreceiver=${receiverLog}`);
  }
  if (!parallelDisabled) {
    const shareLink = await sender.page.locator("#share a").getAttribute("href");
    if (!shareLink?.includes(`?parallel=1#c=${code}`)) {
      throw new Error(`parallel sender did not preserve the experiment flag in its share link: ${shareLink}`);
    }
  }
  const senderMetrics = await sender.page.evaluate(() => window.__kigoLastTransferMetrics || null);
  const receiverMetrics = await receiver.page.evaluate(() => window.__kigoLastTransferMetrics || null);
  const pathLines = [...senderLog.split("\n"), ...receiverLog.split("\n")].filter((line) => line.includes("Path: ") || line.includes("chunk size"));
  if (!pathLines.length) throw new Error(`web-web benchmark did not report selected path\n${senderLog}\n${receiverLog}`);
  const bytes = fs.statSync(src).size;
  if (!senderMetrics || !receiverMetrics) throw new Error(`web-web benchmark did not expose performance telemetry\nsender=${senderLog}\nreceiver=${receiverLog}`);
  if (senderMetrics.payloadBytes !== bytes || receiverMetrics.payloadBytes !== bytes) {
    throw new Error(`web-web telemetry payload mismatch: sender=${senderMetrics.payloadBytes} receiver=${receiverMetrics.payloadBytes} bytes=${bytes}`);
  }
  if (!parallelDisabled && (senderMetrics.sentDataFrameBytes < bytes || receiverMetrics.receivedDataFrameBytes < bytes)) {
    throw new Error(`web-web file did not use parallel data paths: sender=${senderMetrics.sentDataFrameBytes} receiver=${receiverMetrics.receivedDataFrameBytes} bytes=${bytes}`);
  }
  if (parallelDisabled && (senderMetrics.sentDataFrameBytes !== 0 || receiverMetrics.receivedDataFrameBytes !== 0)) {
    throw new Error(`single-path control unexpectedly used a data lane: sender=${senderMetrics.sentDataFrameBytes} receiver=${receiverMetrics.receivedDataFrameBytes}`);
  }
  const durationMS = Math.max(1, Date.now() - started);
  const mibPerSecond = bytes / 1024 / 1024 / (durationMS / 1000);
  console.log(`  ${pathLines.join(" | ")}`);
  console.log(`  telemetry sender=${JSON.stringify(senderMetrics)} receiver=${JSON.stringify(receiverMetrics)}`);
  console.log(`  benchmark bytes=${bytes} duration_ms=${durationMS} throughput_mib_s=${mibPerSecond.toFixed(2)}`);
  if (sender.logs.length) throw new Error(`web-web sender browser logs:\n${sender.logs.join("\n")}`);
  if (receiver.logs.length) throw new Error(`web-web receiver browser logs:\n${receiver.logs.join("\n")}`);
  console.log("ok web->web file");
}

async function webToWebText(browser) {
  console.log("start web->web text");
  const payload = `web to web text payload ${Date.now()}`;
  const code = randomPairingCode();
  const receiver = await startWebReceiver(browser, code);

  const sender = await newPage(browser);
  await sender.page.evaluate((fixedCode) => { window.generateCode = () => fixedCode; }, code);
  await sender.page.click('button[data-tab="text"]');
  await sender.page.fill("#textInput", payload);
  await sender.page.click("#sendText");
  await waitForTransferComplete(sender.page);
  await receiver.done;

  const received = await receiver.page.locator("#textOutput").textContent();
  assertEqual(received, payload, "web->web text mismatch");
  const senderLog = await sender.page.locator("#log").textContent();
  const receiverLog = await receiver.page.locator("#log").textContent();
  if (senderLog.includes("Parallel WebRTC:") || receiverLog.includes("Parallel WebRTC:")) {
    throw new Error(`default web-web text unexpectedly enabled parallel data paths\nsender=${senderLog}\nreceiver=${receiverLog}`);
  }
  if (sender.logs.length) throw new Error(`web-web text sender browser logs:\n${sender.logs.join("\n")}`);
  if (receiver.logs.length) throw new Error(`web-web text receiver browser logs:\n${receiver.logs.join("\n")}`);
  console.log("ok web->web text");
}

async function webToWebParallelLaneFallback(browser) {
  console.log("start web->web parallel lane fallback");
  const dir = path.join(work, "web-to-web-parallel-fallback");
  const src = path.join(dir, "parallel-fallback.bin");
  const dst = path.join(dir, "downloaded.bin");
  fs.mkdirSync(dir, { recursive: true });
  fs.writeFileSync(src, crypto.randomBytes(1024 * 1024));
  const code = randomPairingCode();

  async function fallbackPage(target) {
    return newPage(browser, target, {
      beforeGoto: (page) => page.addInitScript(() => {
        const NativePeerConnection = window.RTCPeerConnection;
        let peerCount = 0;
        window.RTCPeerConnection = new Proxy(NativePeerConnection, {
          construct(Target, args) {
            peerCount++;
            if (peerCount === 3) {
              args[0] = { ...(args[0] || {}), iceTransportPolicy: "relay" };
            }
            return Reflect.construct(Target, args);
          },
        });
      }),
    });
  }

  const receiver = await fallbackPage(`${baseURL}/?parallel=1#c=${code}`);
  const sender = await fallbackPage(`${baseURL}/?parallel=1`);
  await sender.page.setInputFiles("#fileInput", src);
  await sender.page.fill("#fileCode", code);
  await sender.page.click("#sendFile");
  await Promise.all([
    waitForTransferComplete(sender.page),
    waitForTransferComplete(receiver.page),
  ]);
  await saveDownload(receiver.page, () => receiver.page.locator("#downloads a").first().click(), dst);
  assertEqual(sha256(dst), sha256(src), "parallel lane fallback file hash mismatch");
  const senderLog = await sender.page.locator("#log").textContent();
  const receiverLog = await receiver.page.locator("#log").textContent();
  if (!senderLog.includes("Parallel data paths unavailable; continuing on one direct path.") ||
      !receiverLog.includes("Parallel data paths unavailable; continuing on one direct path.")) {
    throw new Error(`parallel lane failure did not preserve the primary path\nsender=${senderLog}\nreceiver=${receiverLog}`);
  }
  if (senderLog.includes("Path: TURN relay") || receiverLog.includes("Path: TURN relay") ||
      senderLog.includes("Parallel WebRTC:") || receiverLog.includes("Parallel WebRTC:")) {
    throw new Error(`parallel lane fallback selected an unexpected route\nsender=${senderLog}\nreceiver=${receiverLog}`);
  }
  const senderMetrics = await sender.page.evaluate(() => window.__kigoLastTransferMetrics || null);
  if (!senderMetrics || senderMetrics.sentDataFrameBytes !== 0 || senderMetrics.pathCount !== 1) {
    throw new Error(`parallel lane fallback telemetry mismatch: ${JSON.stringify(senderMetrics)}`);
  }
  if (sender.logs.length || receiver.logs.length) {
    throw new Error(`parallel lane fallback browser logs: ${[...sender.logs, ...receiver.logs].join(" | ")}`);
  }
  console.log("ok web->web parallel lane fallback");
}

async function webToWebUnorderedReorder(browser) {
  console.log("start web->web unordered reorder");
  await browser.route("**/app.js", async (route) => {
    const response = await route.fetch();
    const body = (await response.text()).replace(
      'const directFirst = negotiation?.pair === "web-web";',
      "const directFirst = false;",
    ).replace(
      'const parallelData = negotiation?.features?.includes(FEATURE_PARALLEL_DATA) === true;',
      "const parallelData = false;",
    ).replace("const ENABLE_UNORDERED_DATA = false;", "const ENABLE_UNORDERED_DATA = true;");
    await route.fulfill({ response, body });
  });
  const dir = path.join(work, "web-to-web-unordered");
  const src = path.join(dir, "unordered.bin");
  const dst = path.join(dir, "downloaded.bin");
  fs.mkdirSync(dir, { recursive: true });
  fs.writeFileSync(src, crypto.randomBytes(1024 * 1024));
  const code = randomPairingCode();
  const receiver = await startWebReceiver(browser, code);
  const sender = await newPage(browser);
  await sender.page.evaluate(() => {
    const createDataChannel = RTCPeerConnection.prototype.createDataChannel;
    RTCPeerConnection.prototype.createDataChannel = function(label, options) {
      const channel = createDataChannel.call(this, label, options);
      if (label !== "kigo-data") return channel;
      const send = channel.send.bind(channel);
      let delayed = true;
      channel.send = (payload) => {
        if (!delayed) {
          send(payload);
          return;
        }
        delayed = false;
        setTimeout(() => send(payload), 100);
      };
      return channel;
    };
  });
  await sender.page.setInputFiles("#fileInput", src);
  await sender.page.fill("#fileCode", code);
  await sender.page.click("#sendFile");
  await Promise.all([
    waitForTransferComplete(sender.page),
    receiver.done,
  ]);
  await saveDownload(receiver.page, () => receiver.page.locator("#downloads a").first().click(), dst);
  assertEqual(sha256(dst), sha256(src), "unordered reordered file hash mismatch");
  const senderLog = await sender.page.locator("#log").textContent();
  const receiverLog = await receiver.page.locator("#log").textContent();
  if (!senderLog.includes("Reliable unordered file channel negotiated.") ||
      !receiverLog.includes("Reliable unordered file channel negotiated.")) {
    throw new Error(`unordered reorder scenario did not negotiate data channel\nsender=${senderLog}\nreceiver=${receiverLog}`);
  }
  if (sender.logs.length || receiver.logs.length) {
    throw new Error(`unordered reorder browser logs: ${[...sender.logs, ...receiver.logs].join(" | ")}`);
  }
  console.log("ok web->web unordered reorder");
}

async function webToWebFallback(browser) {
  console.log("start web->web direct failure fallback");
  const code = randomPairingCode();
  const payload = `web fallback payload ${Date.now()}`;

  async function fallbackPage(target) {
    let iceRequests = 0;
    const tracked = await newPage(browser, target, {
      beforeGoto: async (page) => {
        await page.addInitScript(() => {
          const NativePeerConnection = window.RTCPeerConnection;
          let peerCount = 0;
          window.RTCPeerConnection = new Proxy(NativePeerConnection, {
            construct(Target, args) {
              peerCount++;
              if (peerCount <= 2) {
                args[0] = { ...(args[0] || {}), iceTransportPolicy: "relay" };
              }
              return Reflect.construct(Target, args);
            },
          });
        });
        await page.route("**/api/ice", async (route) => {
          iceRequests++;
          if (iceRequests <= 2) {
            await route.fulfill({
              status: 200,
              contentType: "application/json",
              body: JSON.stringify({ iceServers: [{ urls: ["stun:127.0.0.1:9"] }] }),
            });
            return;
          }
          await route.continue();
        });
      },
    });
    return { ...tracked, iceRequests: () => iceRequests };
  }

  const receiver = await fallbackPage(`${baseURL}/#c=${code}`);
  const sender = await fallbackPage(baseURL);
  await sender.page.click('button[data-tab="text"]');
  await sender.page.fill("#textInput", payload);
  await sender.page.fill("#textCode", code);
  await sender.page.click("#sendText");
  await Promise.all([
    waitForTransferComplete(sender.page, 20000),
    waitForTransferComplete(receiver.page, 20000),
  ]);

  const received = await receiver.page.locator("#textOutput").textContent();
  const senderLog = await sender.page.locator("#log").textContent();
  const receiverLog = await receiver.page.locator("#log").textContent();
  assertEqual(received, payload, "web fallback text mismatch");
  if (!senderLog.includes("Reconnecting 2/3") || !receiverLog.includes("Reconnecting 2/3") ||
      !senderLog.includes("Reconnecting 3/3") || !receiverLog.includes("Reconnecting 3/3")) {
    throw new Error(`both browser roles did not retry after direct failure\nsender=${senderLog}\nreceiver=${receiverLog}`);
  }
  if (!senderLog.includes("Path: TURN relay") || !receiverLog.includes("Path: TURN relay")) {
    throw new Error(`browser retry did not select TURN relay\nsender=${senderLog}\nreceiver=${receiverLog}`);
  }
  if (sender.iceRequests() < 3 || receiver.iceRequests() < 3) {
    throw new Error(`browser retry did not request fresh ICE config: sender=${sender.iceRequests()} receiver=${receiver.iceRequests()}`);
  }
  if (sender.logs.length || receiver.logs.length) {
    throw new Error(`web fallback browser logs: ${[...sender.logs, ...receiver.logs].join(" | ")}`);
  }
  console.log("ok web->web direct failure fallback");
}

async function waitForNoteConnected(page) {
  await page.waitForFunction(() => {
    return document.querySelector("#noteEditor")?.disabled === false;
  }, null, { timeout: 30000 });
}

async function waitForNoteText(page, text) {
  await page.waitForFunction((expected) => {
    return document.querySelector("#noteEditor")?.value === expected;
  }, text, { timeout: 10000 });
}

async function nativeHostToWebNote(browser) {
  console.log("start native host->web note");
  const pad = "Sprint Notes";
  const native = spawnNote(["--pad", pad, "host"]);
  try {
    const code = await waitForPairingCode(native, "native note host");
    const noteLink = `${baseURL}/#n=${code}&p=${encodeURIComponent(pad).replace(/%20/g, "+")}`;
    await waitForOutput(native.output, new RegExp(`Link:\\s*${noteLink.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}`), "native note browser link");
    const { page, logs } = await newPage(browser, noteLink);
    await waitForNoteConnected(page);
    assertEqual(await page.locator("#notePad").inputValue(), pad, "native note deep-link pad mismatch");
    await waitForOutput(native.output, new RegExp(`Connected\\. Pad: ${pad}`), "native note host connection");

    await page.fill("#noteEditor", "browser edit for native host");
    await waitForOutput(native.output, /Remote revision 1:\nbrowser edit for native host/, "browser note update at native host");
    await page.waitForFunction(() => document.querySelector("#noteState")?.textContent === "Synced revision 1", null, { timeout: 10000 });

    await page.click("#leaveNote");
    await page.evaluate(() => localStorage.clear());
    await new Promise((resolve) => setTimeout(resolve, 250));
    if (native.proc.exitCode !== null) throw new Error("native notepad exited when the browser left");

    const later = await newPage(browser, noteLink);
    await waitForNoteConnected(later.page);
    await waitForNoteText(later.page, "browser edit for native host");

    native.proc.stdin.write("native reply for browser\n");
    await waitForNoteText(later.page, "native reply for browser");
    await later.page.waitForFunction(() => document.querySelector("#noteState")?.textContent === "Remote revision 2", null, { timeout: 10000 });

    await later.page.click("#clearNote");
    await waitForOutput(native.output, /Remote revision 3:\n\(empty\)/, "browser note clear at native host");
    await later.page.waitForFunction(() => document.querySelector("#noteState")?.textContent === "Synced revision 3", null, { timeout: 10000 });

    await later.page.click("#leaveNote");
    native.proc.stdin.write("/quit\n");
    const codeAfterLeave = await withTimeout("native note host exit", 10000, () => waitProc(native.proc));
    const { stdout, stderr } = native.output();
    assertEqual(codeAfterLeave, 0, `native note host exited non-zero\nstdout=${stdout}\nstderr=${stderr}`);
    if (logs.length) throw new Error(`native host->web note browser logs:\n${logs.join("\n")}`);
    if (later.logs.length) throw new Error(`native host->later web note browser logs:\n${later.logs.join("\n")}`);
    console.log("ok native host->web note");
  } finally {
    await stopProc(native.proc);
  }
}

async function webHostToNativeNote(browser) {
  console.log("start web host->native note");
  const code = randomPairingCode();
  const pad = "Release Draft";
  const { page, logs } = await newPage(browser);
  await page.evaluate((fixedCode) => { window.generateCode = () => fixedCode; }, code);
  await page.click('button[data-tab="note"]');
  await page.fill("#notePad", pad);
  await page.click("#hostNote");
  await page.waitForFunction(({ expectedCode, expectedPad }) => {
    const share = document.querySelector("#share")?.textContent || "";
    const href = document.querySelector("#share a")?.getAttribute("href") || "";
    return share.includes(expectedCode) && share.includes(expectedPad) && href.includes("#n=") && href.includes("p=Release+Draft");
  }, { expectedCode: code, expectedPad: pad }, { timeout: 5000 });

  const native = spawnNote(["--pad", pad, "join", code]);
  try {
    await waitForNoteConnected(page);
    await waitForOutput(native.output, new RegExp(`Connected\\. Pad: ${pad}`), "native note join connection");

    await page.fill("#noteEditor", "browser host update");
    await waitForOutput(native.output, /Remote revision 1:\nbrowser host update/, "browser host update at native join");

    native.proc.stdin.write("native join reply\n");
    await waitForNoteText(page, "native join reply");
    await page.waitForFunction(() => document.querySelector("#noteState")?.textContent === "Remote revision 2", null, { timeout: 10000 });

    native.proc.stdin.write("/clear\n");
    await waitForNoteText(page, "");
    await page.waitForFunction(() => document.querySelector("#noteState")?.textContent === "Remote revision 3", null, { timeout: 10000 });

    native.proc.stdin.write("/quit\n");
    const codeAfterQuit = await withTimeout("native note join exit", 10000, () => waitProc(native.proc));
    const { stdout, stderr } = native.output();
    assertEqual(codeAfterQuit, 0, `native note join exited non-zero\nstdout=${stdout}\nstderr=${stderr}`);
    await waitForNoteConnected(page);
    await page.fill("#noteEditor", "browser remains available");
    await page.waitForFunction(() => document.querySelector("#noteState")?.textContent === "Synced revision 4", null, { timeout: 10000 });
    await page.click("#leaveNote");
    if (logs.length) throw new Error(`web host->native note browser logs:\n${logs.join("\n")}`);
    console.log("ok web host->native note");
  } finally {
    await stopProc(native.proc);
  }
}

async function webToWebNote(browser) {
  console.log("start web->web note");
  const code = "WEB-NOTE-CUSTOM-2026";
  const pad = "Browser Board";
  const host = await newPage(browser);
  await host.page.click('button[data-tab="note"]');
  await host.page.fill("#noteCode", code.toLowerCase());
  await host.page.fill("#notePad", pad);
  await host.page.click("#hostNote");

  const join = await newPage(browser, `${baseURL}/#n=${code}&p=Browser+Board`);
  await Promise.all([waitForNoteConnected(host.page), waitForNoteConnected(join.page)]);
  assertEqual(await host.page.locator("#notePad").inputValue(), pad, "web note host pad mismatch");
  assertEqual(await join.page.locator("#notePad").inputValue(), pad, "web note join pad mismatch");

  await host.page.fill("#noteEditor", "host browser document");
  await waitForNoteText(join.page, "host browser document");
  await host.page.waitForFunction(() => document.querySelector("#noteState")?.textContent === "Synced revision 1", null, { timeout: 10000 });

  await join.page.fill("#noteEditor", "join browser document");
  await waitForNoteText(host.page, "join browser document");
  await join.page.waitForFunction(() => document.querySelector("#noteState")?.textContent === "Synced revision 2", null, { timeout: 10000 });

  await join.page.click("#clearNote");
  await waitForNoteText(host.page, "");
  await join.page.waitForFunction(() => document.querySelector("#noteState")?.textContent === "Synced revision 3", null, { timeout: 10000 });

  await host.page.click("#leaveNote");
  await waitForNoteConnected(join.page);
  await join.page.fill("#noteEditor", "still available while alone");
  await join.page.waitForFunction(() => document.querySelector("#noteState")?.textContent === "Synced revision 4", null, { timeout: 10000 });
  await join.page.click("#leaveNote");
  await join.page.evaluate(() => localStorage.clear());

  const later = await newPage(browser, `${baseURL}/#n=${code}&p=Browser+Board`);
  await waitForNoteConnected(later.page);
  await waitForNoteText(later.page, "still available while alone");
  await later.page.waitForFunction((expectedCode) => {
    return document.querySelector(".note-recent-code")?.textContent === expectedCode;
  }, code, { timeout: 10000 });
  await later.page.locator(".note-recent-row input[type=checkbox]").check();
  const recentCatalog = await later.page.evaluate(() => localStorage.getItem("kigo-note-recent-v1"));
  if (!recentCatalog || !recentCatalog.includes(code) || !recentCatalog.includes("Browser Board") ||
      !recentCatalog.includes('"favorite":true') || recentCatalog.includes("still available while alone")) {
    throw new Error(`browser recent notepad catalog mismatch: ${recentCatalog}`);
  }
  await later.page.click("#leaveNote");
  await later.page.waitForFunction(() => {
    return document.querySelector("#noteEditor")?.disabled === true &&
      document.querySelector("[data-note-recent-open]")?.disabled === false;
  }, null, { timeout: 10000 });
  await later.page.click("[data-note-recent-open]");
  await waitForNoteConnected(later.page);
  await waitForNoteText(later.page, "still available while alone");
  await later.page.click("#leaveNote");
  await later.page.waitForFunction(() => document.querySelector("[data-note-recent-open]")?.disabled === false, null, { timeout: 10000 });
  await later.page.locator(".note-recent-row button", { hasText: "Remove" }).click();
  await later.page.waitForFunction(() => document.querySelectorAll(".note-recent-row").length === 0);
  if (host.logs.length) throw new Error(`web note host browser logs:\n${host.logs.join("\n")}`);
  if (join.logs.length) throw new Error(`web note join browser logs:\n${join.logs.join("\n")}`);
  if (later.logs.length) throw new Error(`web note later browser logs:\n${later.logs.join("\n")}`);
  console.log("ok web->web note");
}

async function webPersistentNoteIsolation(browser) {
  console.log("start web persistent note isolation");
  const code = randomPairingCode();
  const first = await newPage(browser);
  await first.page.evaluate((fixedCode) => { window.generateCode = () => fixedCode; }, code);
  await first.page.click('button[data-tab="note"]');
  await first.page.fill("#notePad", "First Pad");
  await first.page.click("#hostNote");
  await waitForNoteConnected(first.page);
  await first.page.fill("#noteEditor", "first pad secret");
  await first.page.waitForFunction(() => document.querySelector("#noteState")?.textContent === "Synced revision 1", null, { timeout: 10000 });

  const isolated = await newPage(browser, `${baseURL}/#n=${code}&p=Second+Pad`);
  await waitForNoteConnected(isolated.page);
  assertEqual(await isolated.page.locator("#noteEditor").inputValue(), "", "persistent note pad isolation failed");
  await first.page.click("#leaveNote");
  await isolated.page.click("#leaveNote");
  if (first.logs.length) throw new Error(`first persistent note browser logs:\n${first.logs.join("\n")}`);
  if (isolated.logs.length) throw new Error(`isolated persistent note browser logs:\n${isolated.logs.join("\n")}`);
  console.log("ok web persistent note isolation");
}

async function webProtocolGuards(browser) {
  console.log("start web protocol guards");
  const { page, logs } = await newPage(browser);
  const result = await page.evaluate(async () => {
    const hexOf = (bytes) => [...bytes].map((b) => b.toString(16).padStart(2, "0")).join("");
    const data = new Uint8Array(4 * 1024 * 1024 + 123);
    for (let i = 0; i < data.length; i++) data[i] = i & 0xff;
    const file = new File([data], "hash-vector.bin");
    const base64Probe = data.slice(0, 128 * 1024 + 17);
    const base64RoundTripHash = await bytesSHA256(base64ToBytes(bytesToBase64(base64Probe)));
    const subtle = hexOf(new Uint8Array(await crypto.subtle.digest("SHA-256", data)));
    const compressionSource = new TextEncoder().encode("compressible browser payload\n".repeat(2000));
    const compressionEncoded = await compressGzipChunk(compressionSource);
    const compressionDecoded = await decodeTransferChunk(compressionEncoded, "gzip", "gzip");
    let compressionGuard = "";
    try {
      await decodeTransferChunk(compressionEncoded, "gzip", "");
    } catch (err) {
      compressionGuard = err.message;
    }
    const item = { name: "guard.txt", size: 3 };
    const guardErrors = [];
    for (const args of [[-1, 1, 0], [2, 1, 0], [0, 4, 0]]) {
      try {
        validateChunkBounds(item, args[0], args[1], args[2]);
        guardErrors.push("");
      } catch (err) {
        guardErrors.push(err.message);
      }
    }
    const streamErrors = [];
    for (const msg of [
      { item: 0 },
      { item: 0, stream: 0 },
      { item: 0, stream: 1 },
    ]) {
      try {
        validateChunkStream(msg);
        streamErrors.push("");
      } catch (err) {
        streamErrors.push(err.message);
      }
    }
    const streamStateErrors = [];
    const streamManifest = { version: VERSION, items: [{ kind: "file", name: "a.bin", size: 1, chunk_size: CHUNK_SIZE }] };
    for (const scenario of [
      () => {
        const states = new Map();
        acceptStreamOpen(streamManifest, states, { type: "stream_open", item: 0, stream: 0 });
        acceptStreamOpen(streamManifest, states, { type: "stream_open", item: 0, stream: 0 });
      },
      () => {
        acceptStreamEnd(streamManifest, new Map(), { type: "stream_end", item: 0, stream: 0 });
      },
      () => {
        const states = new Map();
        acceptStreamOpen(streamManifest, states, { type: "stream_open", item: 0, stream: 0 });
        acceptStreamEnd(streamManifest, states, { type: "stream_end", item: 0, stream: 0 });
        acceptChunkStream(streamManifest, states, { type: "chunk", item: 0, stream: 0 });
      },
    ]) {
      try {
        scenario();
        streamStateErrors.push("");
      } catch (err) {
        streamStateErrors.push(err.message);
      }
    }
    const resumeErrors = [];
    const resumeItems = [{ kind: "file", name: "resume.bin", size: 10, chunk_size: CHUNK_SIZE, resume_supported: true }];
    let resumeClamp = -1;
    for (const entry of [
      { item: 0, stream: 1, offset: 0 },
      { item: 0, stream: 0, offset: -1 },
      { item: 0, stream: 0, offset: 1, prefix_sha256: "bad" },
    ]) {
      try {
        validateResumeEntry(resumeItems, entry);
        resumeErrors.push("");
      } catch (err) {
        resumeErrors.push(err.message);
      }
    }
    resumeClamp = validateResumeEntry(resumeItems, { item: 0, stream: 0, offset: 99 });
    const memoryManifest = {
      version: VERSION,
      items: [{ kind: "file", name: "memory.bin", size: 3, sha256: await bytesSHA256(new TextEncoder().encode("abc")), chunk_size: CHUNK_SIZE, resume_supported: true }],
      streams: [{ id: 7, item: 0 }],
    };
    const memoryPlan = createMuxPlan(memoryManifest);
    const memoryStore = createMemoryFileStore(memoryManifest, memoryPlan);
    await memoryStore.applyResumeAccept([{ item: 0, stream: 7, offset: 0 }]);
    await memoryStore.writeChunk(0, 0, new TextEncoder().encode("abc"));
    const memoryFiles = await memoryStore.finalize();
    const memoryHash = await fileSHA256(memoryFiles.get(0).blob);
    const partialOPFSManifest = {
      version: VERSION,
      items: [{ kind: "file", name: "partial-opfs.bin", size: 3, sha256: memoryManifest.items[0].sha256, chunk_size: CHUNK_SIZE, resume_supported: true }],
    };
    const partialOPFSPlan = createMuxPlan(partialOPFSManifest);
    const storage = navigator.storage;
    const getDirectoryDescriptor = Object.getOwnPropertyDescriptor(storage, "getDirectory");
    const fakeFileHandle = {
      kind: "file",
      getFile: async () => new File([], "partial-opfs.part", { lastModified: Date.now() }),
    };
    const fakeDirectory = {
      getFileHandle: async () => fakeFileHandle,
      async *entries() {},
    };
    let partialOPFSPersistent = null;
    let partialOPFSHash = "";
    let partialOPFSError = "";
    Object.defineProperty(storage, "getDirectory", {
      configurable: true,
      value: async () => ({ getDirectoryHandle: async () => fakeDirectory }),
    });
    try {
      const partialOPFSStore = await createBrowserFileStore(partialOPFSManifest, partialOPFSPlan);
      partialOPFSPersistent = partialOPFSStore.persistent;
      await partialOPFSStore.applyResumeAccept([{ item: 0, stream: 0, offset: 0 }]);
      await partialOPFSStore.writeChunk(0, 0, new TextEncoder().encode("abc"));
      const completed = await partialOPFSStore.finalize();
      partialOPFSHash = await fileSHA256(completed.get(0).blob);
      await partialOPFSStore.cleanup();
    } catch (err) {
      partialOPFSError = err.message;
    } finally {
      if (getDirectoryDescriptor) {
        Object.defineProperty(storage, "getDirectory", getDirectoryDescriptor);
      } else {
        delete storage.getDirectory;
      }
    }
    const turnRouteDescription = describeSelectedWebRTCPath(new Map([
      ["transport", { type: "transport", selectedCandidatePairId: "pair" }],
      ["pair", { type: "candidate-pair", state: "succeeded", nominated: true, localCandidateId: "local", remoteCandidateId: "remote" }],
      ["local", { type: "local-candidate", candidateType: "relay", protocol: "udp" }],
      ["remote", { type: "remote-candidate", candidateType: "relay", protocol: "udp" }],
    ]));
    const directRouteDescription = describeSelectedWebRTCPath(new Map([
      ["pair", { type: "candidate-pair", state: "succeeded", nominated: true, localCandidateId: "local", remoteCandidateId: "remote" }],
      ["local", { type: "local-candidate", candidateType: "host", protocol: "udp" }],
      ["remote", { type: "remote-candidate", candidateType: "srflx", protocol: "udp" }],
    ]));
    resetOutput();
    const streamProgress = createStreamProgress("Testing", resumeItems, new Map([[0, 8]]));
    streamProgress.show("baseline");
    streamProgress.addStream(0, 99);
    const streamProgressText = document.querySelector("#progress").textContent;
    const scheduler = createWeightedScheduler([{ id: 10, weight: 2 }, { id: 20, weight: 1 }], 4);
    const schedulerSequence = [];
    for (let i = 0; i < 6; i++) {
      const turn = scheduler.next(4);
      schedulerSequence.push(turn.streamID);
      scheduler.commit(turn.streamID, turn.budget, false);
    }
    const adaptiveBudgets = [
      adaptiveSendBudget(64 * 1024, 16 * 1024, {}),
      adaptiveSendBudget(64 * 1024, 16 * 1024, { bufferedBytes: 1 * 1024 * 1024, bufferLimit: 4 * 1024 * 1024 }),
      adaptiveSendBudget(64 * 1024, 16 * 1024, { bufferedBytes: 2 * 1024 * 1024, bufferLimit: 4 * 1024 * 1024 }),
      adaptiveSendBudget(64 * 1024, 16 * 1024, { lastWaitMs: 25 }),
    ];
    const manifestErrors = [];
    for (const manifest of [
      { version: VERSION, items: [{ kind: "file", name: "bad.txt", size: -1, chunk_size: CHUNK_SIZE }] },
      { version: VERSION, items: [
        { kind: "file", name: "same.txt", size: 0, chunk_size: CHUNK_SIZE },
        { kind: "file", name: "same.txt", size: 0, chunk_size: CHUNK_SIZE },
      ] },
      { version: VERSION, items: [
        { kind: "directory", name: "shared", size: 0, chunk_size: CHUNK_SIZE },
        { kind: "file", name: "shared", size: 0, chunk_size: CHUNK_SIZE },
      ] },
      { version: VERSION, items: [{ kind: "text", name: "message.txt", size: 0, sha256: "bad", chunk_size: CHUNK_SIZE }] },
      { version: VERSION, items: [{ kind: "text", name: "message.txt", size: 0, chunk_size: MAX_CHUNK_SIZE + 1 }] },
      { version: VERSION, items: [{ kind: "file", name: "../escape.txt", size: 0, chunk_size: CHUNK_SIZE }] },
      { version: VERSION, items: [{ kind: "symlink", name: "link", target: "../outside", size: 0, chunk_size: CHUNK_SIZE }] },
    ]) {
      try {
        validateManifest(manifest);
        manifestErrors.push("");
      } catch (err) {
        manifestErrors.push(err.message);
      }
    }
    const envelopeErrors = [];
    for (const env of [
      { version: VERSION + 1, seq: 0 },
      { version: VERSION, seq: 1 },
      { version: VERSION, seq: -1 },
    ]) {
      try {
        validateEnvelope(env, { nextSeq: 0 });
        envelopeErrors.push("");
      } catch (err) {
        envelopeErrors.push(err.message);
      }
    }
    const helloErrors = [];
    for (const [msg, wantType] of [
      [{ type: "hello", version: VERSION + 1, sender_nonce: "sender" }, "hello"],
      [{ type: "hello", version: VERSION }, "hello"],
      [{ type: "hello_ack", version: VERSION + 1, receiver_nonce: "receiver" }, "hello_ack"],
      [{ type: "hello_ack", version: VERSION }, "hello_ack"],
      [{ type: "hello_ack", version: VERSION, receiver_nonce: "receiver", compression: "zstd" }, "hello_ack"],
    ]) {
      try {
        validateHello(msg, wantType);
        helloErrors.push("");
      } catch (err) {
        helloErrors.push(err.message);
      }
    }
    const noteFrameErrors = [];
    for (const frame of [
      { type: "update", version: VERSION + 1, pad: NOTE_DEFAULT_PAD, text: "bad", revision: 1, timestamp: 1 },
      { type: "unsupported", version: VERSION, pad: NOTE_DEFAULT_PAD },
      { type: "update", version: VERSION, pad: NOTE_DEFAULT_PAD, text: "x".repeat(NOTE_MAX_TEXT_BYTES + 1), revision: 1, timestamp: 1 },
    ]) {
      try {
        validateNoteFrame(frame);
        noteFrameErrors.push("");
      } catch (err) {
        noteFrameErrors.push(err.message);
      }
    }
    let noteEnvelopeSequenceError = "";
    try {
      await recvNoteEncrypted({
        recv: async () => JSON.stringify({ version: VERSION, seq: 2, ciphertext: "AA==" }),
      }, {
        recv: { nextSeq: 0 },
      });
    } catch (err) {
      noteEnvelopeSequenceError = err.message;
    }
    const noteConflictOrder = [
      compareNoteDocuments(
        { revision: 2, timestamp: 1, text: "a" },
        { revision: 1, timestamp: 999, text: "z" },
      ),
      compareNoteDocuments(
        { revision: 2, timestamp: 2, text: "a" },
        { revision: 2, timestamp: 1, text: "z" },
      ),
      compareNoteDocuments(
        { revision: 2, timestamp: 2, text: "z" },
        { revision: 2, timestamp: 2, text: "a" },
      ),
      compareNoteDocuments(
        { revision: 2, timestamp: 2, text: "same" },
        { revision: 2, timestamp: 2, text: "same" },
      ),
    ];
    return {
      empty: await bytesSHA256(new Uint8Array()),
      abc: await bytesSHA256(new TextEncoder().encode("abc")),
      file: await fileSHA256(file),
      base64ProbeHash: await bytesSHA256(base64Probe),
      base64RoundTripHash,
      subtle,
      compressionSupported: supportedCompressions().includes("gzip"),
      compressionEncodedSize: compressionEncoded.length,
      compressionDecodedHash: await bytesSHA256(compressionDecoded),
      compressionSourceHash: await bytesSHA256(compressionSource),
      compressionGuard,
      guardErrors,
      streamErrors,
      streamStateErrors,
      resumeErrors,
      resumeClamp,
      memoryHash,
      partialOPFSPersistent,
      partialOPFSHash,
      partialOPFSError,
      turnRouteDescription,
      directRouteDescription,
      streamProgressText,
      schedulerSequence,
      adaptiveBudgets,
      manifestErrors,
      envelopeErrors,
      helloErrors,
      featureSelections: [
        supportedTransferFeatures(0),
        supportedTransferFeatures(1),
        selectTransferFeatures([FEATURE_DEFERRED_FILE_SHA256, FEATURE_LARGE_CHUNKS], 0),
        supportedTransferFeatures(0, true, true),
        selectTransferFeatures([FEATURE_DEFERRED_FILE_SHA256, FEATURE_UNORDERED_DATA], 0, true, true),
      ],
      noteFrameErrors,
      noteEnvelopeSequenceError,
      noteConflictOrder,
      pairingCodes: [
        normalizeCode(" k7m9-q2 "),
        normalizeCode(" project-Alpha-2026 "),
        validPairingCode("PROJECT-ALPHA-2026"),
        validPairingCode("BAD--CODE"),
      ],
    };
  });
  assertEqual(result.empty, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", "empty sha256 mismatch");
  assertEqual(result.abc, "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", "abc sha256 mismatch");
  assertEqual(result.file, result.subtle, "streaming file sha256 mismatch");
  assertEqual(result.base64RoundTripHash, result.base64ProbeHash, "base64 round trip mismatch");
  if (!result.compressionSupported) throw new Error("browser gzip streams are unavailable");
  if (result.compressionEncodedSize >= 1000) throw new Error(`browser gzip did not compress payload: ${result.compressionEncodedSize}`);
  assertEqual(result.compressionDecodedHash, result.compressionSourceHash, "browser gzip round trip mismatch");
  if (!result.compressionGuard.includes("without negotiated compression")) throw new Error(`compression negotiation guard mismatch: ${result.compressionGuard}`);
  if (!result.guardErrors[0].includes("negative chunk offset")) throw new Error(`negative offset guard mismatch: ${result.guardErrors[0]}`);
  if (!result.guardErrors[1].includes("unexpected chunk offset")) throw new Error(`unexpected offset guard mismatch: ${result.guardErrors[1]}`);
  if (!result.guardErrors[2].includes("chunk exceeds declared size")) throw new Error(`oversized chunk guard mismatch: ${result.guardErrors[2]}`);
  if (result.streamErrors[0] !== "") throw new Error(`missing stream should be accepted: ${result.streamErrors[0]}`);
  if (result.streamErrors[1] !== "") throw new Error(`matching stream should be accepted: ${result.streamErrors[1]}`);
  if (!result.streamErrors[2].includes("does not match item")) throw new Error(`stream guard mismatch: ${result.streamErrors[2]}`);
  if (!result.streamStateErrors[0].includes("opened more than once")) throw new Error(`duplicate stream_open guard mismatch: ${result.streamStateErrors[0]}`);
  if (!result.streamStateErrors[1].includes("ended before open")) throw new Error(`stream_end before open guard mismatch: ${result.streamStateErrors[1]}`);
  if (!result.streamStateErrors[2].includes("chunk arrived after stream")) throw new Error(`chunk after stream_end guard mismatch: ${result.streamStateErrors[2]}`);
  if (!result.resumeErrors[0].includes("does not match item")) throw new Error(`resume stream guard mismatch: ${result.resumeErrors[0]}`);
  if (!result.resumeErrors[1].includes("negative resume offset")) throw new Error(`resume offset guard mismatch: ${result.resumeErrors[1]}`);
  if (!result.resumeErrors[2].includes("invalid resume prefix sha256")) throw new Error(`resume hash guard mismatch: ${result.resumeErrors[2]}`);
  if (result.resumeClamp !== 10) throw new Error(`resume clamp mismatch: ${result.resumeClamp}`);
  assertEqual(result.memoryHash, result.abc, "memory receive store mismatch");
  if (result.partialOPFSPersistent !== false || result.partialOPFSError !== "") {
    throw new Error(`partial OPFS did not fall back to memory: persistent=${result.partialOPFSPersistent} error=${result.partialOPFSError}`);
  }
  assertEqual(result.partialOPFSHash, result.abc, "partial OPFS fallback hash mismatch");
  assertEqual(result.turnRouteDescription, "TURN relay (UDP)", "TURN route description mismatch");
  assertEqual(result.directRouteDescription, "direct P2P (UDP; host/srflx)", "direct route description mismatch");
  if (!result.streamProgressText.includes("100%") || !result.streamProgressText.includes("10 B / 10 B")) {
    throw new Error(`stream progress clamp mismatch: ${result.streamProgressText}`);
  }
  assertEqual(result.schedulerSequence.join(","), "10,10,20,10,10,20", "weighted scheduler order mismatch");
  assertEqual(result.adaptiveBudgets.join(","), "65536,32768,16384,16384", "adaptive send budget mismatch");
  if (!result.manifestErrors[0].includes("invalid size")) throw new Error(`manifest size guard mismatch: ${result.manifestErrors[0]}`);
  if (!result.manifestErrors[1].includes("duplicate path")) throw new Error(`manifest duplicate guard mismatch: ${result.manifestErrors[1]}`);
  if (!result.manifestErrors[2].includes("duplicate path")) throw new Error(`manifest cross-kind duplicate guard mismatch: ${result.manifestErrors[2]}`);
  if (!result.manifestErrors[3].includes("invalid sha256")) throw new Error(`manifest sha256 guard mismatch: ${result.manifestErrors[3]}`);
  if (!result.manifestErrors[4].includes("invalid chunk size")) throw new Error(`manifest chunk-size guard mismatch: ${result.manifestErrors[4]}`);
  if (!result.manifestErrors[5].includes("unsafe file path")) throw new Error(`manifest path guard mismatch: ${result.manifestErrors[5]}`);
  if (!result.manifestErrors[6].includes("unsafe symlink target")) throw new Error(`manifest symlink guard mismatch: ${result.manifestErrors[6]}`);
  if (!result.envelopeErrors[0].includes("unsupported envelope version")) throw new Error(`envelope version guard mismatch: ${result.envelopeErrors[0]}`);
  if (!result.envelopeErrors[1].includes("unexpected envelope sequence")) throw new Error(`envelope sequence guard mismatch: ${result.envelopeErrors[1]}`);
  if (!result.envelopeErrors[2].includes("invalid envelope sequence")) throw new Error(`envelope invalid sequence guard mismatch: ${result.envelopeErrors[2]}`);
  if (!result.helloErrors[0].includes("unsupported hello version")) throw new Error(`hello version guard mismatch: ${result.helloErrors[0]}`);
  if (!result.helloErrors[1].includes("invalid sender hello nonce")) throw new Error(`hello nonce guard mismatch: ${result.helloErrors[1]}`);
  if (!result.helloErrors[2].includes("unsupported hello version")) throw new Error(`hello_ack version guard mismatch: ${result.helloErrors[2]}`);
  if (!result.helloErrors[3].includes("invalid receiver hello ack nonce")) throw new Error(`hello_ack nonce guard mismatch: ${result.helloErrors[3]}`);
  if (!result.helloErrors[4].includes("unsupported hello compression")) throw new Error(`hello compression guard mismatch: ${result.helloErrors[4]}`);
  assertEqual(result.featureSelections[0].join(","), "deferred-file-sha256,binary-chunks-v1,large-chunks-v1", "full transfer feature support mismatch");
  assertEqual(result.featureSelections[1].join(","), "deferred-file-sha256,binary-chunks-v1", "small-message transfer feature support mismatch");
  assertEqual(result.featureSelections[2].join(","), "deferred-file-sha256", "large chunks must require binary chunks");
  assertEqual(result.featureSelections[3].join(","), "deferred-file-sha256,binary-chunks-v1,large-chunks-v1,unordered-data-v1", "unordered data feature support mismatch");
  assertEqual(result.featureSelections[4].join(","), "deferred-file-sha256", "unordered data must require binary chunks");
  if (!result.noteFrameErrors[0].includes("unsupported note frame version")) throw new Error(`note frame version guard mismatch: ${result.noteFrameErrors[0]}`);
  if (!result.noteFrameErrors[1].includes("unsupported note frame type")) throw new Error(`note frame type guard mismatch: ${result.noteFrameErrors[1]}`);
  if (!result.noteFrameErrors[2].includes("note text exceeds")) throw new Error(`note text size guard mismatch: ${result.noteFrameErrors[2]}`);
  if (!result.noteEnvelopeSequenceError.includes("unexpected note envelope sequence")) {
    throw new Error(`note envelope sequence guard mismatch: ${result.noteEnvelopeSequenceError}`);
  }
  assertEqual(result.noteConflictOrder.join(","), "1,1,1,0", "note conflict ordering mismatch");
  assertEqual(result.pairingCodes.join(","), "K7M9Q2,PROJECT-ALPHA-2026,true,false", "pairing code normalization mismatch");
  if (logs.length) throw new Error(`web protocol guard browser logs:\n${logs.join("\n")}`);
  console.log("ok web protocol guards");
}

async function webErrorHandling(browser) {
  console.log("start web error handling");
  const { page, logs } = await newPage(browser);
  await page.click('button[data-tab="text"]');
  await page.fill("#textInput", "trigger error handling");
  await page.evaluate(() => {
    runSender = async () => {
      await new Promise((resolve) => setTimeout(resolve, 200));
      throw new Error("smoke failure");
    };
  });
  await page.click("#sendText");
  await page.waitForFunction(() => document.querySelector("#sendText")?.disabled === true, null, { timeout: 1000 });
  await page.waitForFunction(() => {
    const log = document.querySelector("#log")?.textContent || "";
    return log.includes("Error: smoke failure") && document.querySelector("#sendText")?.disabled === false;
  }, null, { timeout: 3000 });
  const timeoutMessage = await page.evaluate(async () => {
    try {
      await globalThis.kigoWebRTC.withTimeout(new Promise(() => {}), "smoke timeout", 10);
      return "";
    } catch (err) {
      return err.message;
    }
  });
  assertEqual(timeoutMessage, "smoke timeout timed out after 1s", "web timeout helper message mismatch");
  if (logs.length) throw new Error(`web error handling browser logs:\n${logs.join("\n")}`);
  console.log("ok web error handling");
}

async function webCancelHandling(browser) {
  console.log("start web cancel handling");
  const { page, logs } = await newPage(browser);
  await page.click('button[data-tab="text"]');
  await page.fill("#textInput", "cancel me");
  await page.evaluate(() => {
    runSender = async (_items, task) => {
      await new Promise((resolve, reject) => {
        const timer = setInterval(() => {
          if (task.canceled) {
            clearInterval(timer);
            reject(new Error("transfer canceled"));
          }
        }, 20);
      });
    };
  });
  await page.click("#sendText");
  await page.waitForFunction(() => document.querySelector("#cancelButton")?.disabled === false, null, { timeout: 1000 });
  await page.click("#cancelButton");
  await page.waitForFunction(() => {
    const log = document.querySelector("#log")?.textContent || "";
    return log.includes("Canceled.") &&
      document.querySelector("#sendText")?.disabled === false &&
      document.querySelector("#cancelButton")?.classList.contains("hidden");
  }, null, { timeout: 3000 });
  const friendly = await page.evaluate(() => friendlyError(new Error("room is full")));
  if (!friendly.includes("already in use")) throw new Error(`friendly room-full error mismatch: ${friendly}`);
  if (logs.length) throw new Error(`web cancel handling browser logs:\n${logs.join("\n")}`);
  console.log("ok web cancel handling");
}

async function webToNativeResumeFile(browser) {
  console.log("start web->native resume file");
  const dir = path.join(work, "web-to-native-resume-file");
  const src = path.join(dir, "web-to-native-resume.txt");
  const out = path.join(dir, "out");
  const part = path.join(out, "web-to-native-resume.txt.kigopart");
  const saved = path.join(out, "web-to-native-resume.txt");
  let body = "";
  for (let i = 0; i < 12000; i++) body += `resume-web-line-${String(i).padStart(5, "0")} abcdefghijklmnopqrstuvwxyz\n`;
  writeFile(src, body);
  fs.mkdirSync(out, { recursive: true });
  fs.writeFileSync(part, fs.readFileSync(src).subarray(0, 180000));
  const { logs, stdout, stderr, pageLog } = await transferWebInputToNative(browser, {
    src, out, label: "web->native resume",
  });
  assertEqual(sha256(saved), sha256(src), "web->native resume file hash mismatch");
  if (fs.existsSync(part)) throw new Error("web->native resume part file still exists after completion");
  if (!stdout.includes("resuming web-to-native-resume.txt from 180000/")) {
    throw new Error(`native receiver did not log resume offset\nstdout=${stdout}\nstderr=${stderr}`);
  }
  if (!pageLog.includes("sent ")) throw new Error(`web sender did not log sent progress\n${pageLog}`);
  if (logs.length) throw new Error(`web->native resume browser logs:\n${logs.join("\n")}`);
  console.log("ok web->native resume file");
}

async function webToNativeCorruptResumeFile(browser) {
  console.log("start web->native corrupt resume file");
  const dir = path.join(work, "web-to-native-corrupt-resume-file");
  const src = path.join(dir, "web-to-native-corrupt-resume.txt");
  const out = path.join(dir, "out");
  const part = path.join(out, "web-to-native-corrupt-resume.txt.kigopart");
  const saved = path.join(out, "web-to-native-corrupt-resume.txt");
  let body = "";
  for (let i = 0; i < 8000; i++) body += `corrupt-resume-line-${String(i).padStart(5, "0")} abcdefghijklmnopqrstuvwxyz\n`;
  writeFile(src, body);
  fs.mkdirSync(out, { recursive: true });
  fs.writeFileSync(part, Buffer.alloc(120000, 0x78));
  const { logs, stdout, stderr, pageLog } = await transferWebInputToNative(browser, {
    src, out, label: "web->native corrupt resume",
  });
  assertEqual(sha256(saved), sha256(src), "web->native corrupt resume file hash mismatch");
  if (fs.existsSync(part)) throw new Error("web->native corrupt resume part file still exists after completion");
  if (!stdout.includes("sender accepted web-to-native-corrupt-resume.txt resume at 0 bytes")) {
    throw new Error(`native receiver did not restart corrupt part\nstdout=${stdout}\nstderr=${stderr}`);
  }
  if (!pageLog.includes("Resume prefix mismatch")) {
    throw new Error(`web sender did not report prefix mismatch\n${pageLog}`);
  }
  if (logs.length) throw new Error(`web->native corrupt resume browser logs:\n${logs.join("\n")}`);
  console.log("ok web->native corrupt resume file");
}

function readZipEntries(zipFile) {
  const data = fs.readFileSync(zipFile);
  const entries = new Map();
  let offset = 0;
  while (offset + 30 <= data.length && data.readUInt32LE(offset) === 0x04034b50) {
    const flags = data.readUInt16LE(offset + 6);
    const method = data.readUInt16LE(offset + 8);
    const compressedSize = data.readUInt32LE(offset + 18);
    const uncompressedSize = data.readUInt32LE(offset + 22);
    const nameLen = data.readUInt16LE(offset + 26);
    const extraLen = data.readUInt16LE(offset + 28);
    if (flags & 0x0008) throw new Error("zip data descriptors are not supported by this smoke test");
    const nameStart = offset + 30;
    const name = data.slice(nameStart, nameStart + nameLen).toString("utf8");
    const bodyStart = nameStart + nameLen + extraLen;
    const body = data.slice(bodyStart, bodyStart + compressedSize);
    if (method === 0) entries.set(name, body);
    else if (method === 8) entries.set(name, zlib.inflateRawSync(body));
    else throw new Error(`unsupported zip method ${method} for ${name}`);
    if (entries.get(name).length !== uncompressedSize) throw new Error(`zip size mismatch for ${name}`);
    offset = bodyStart + compressedSize;
  }
  return entries;
}

function readZipModes(zipFile) {
  const data = fs.readFileSync(zipFile);
  const modes = new Map();
  for (let offset = 0; offset + 46 <= data.length;) {
    const signature = data.indexOf(Buffer.from([0x50, 0x4b, 0x01, 0x02]), offset);
    if (signature < 0 || signature + 46 > data.length) break;
    const nameLen = data.readUInt16LE(signature + 28);
    const extraLen = data.readUInt16LE(signature + 30);
    const commentLen = data.readUInt16LE(signature + 32);
    const name = data.slice(signature + 46, signature + 46 + nameLen).toString("utf8");
    modes.set(name, data.readUInt32LE(signature + 38) >>> 16);
    offset = signature + 46 + nameLen + extraLen + commentLen;
  }
  return modes;
}

async function nativeToWebDirectory(browser) {
  console.log("start native->web directory zip");
  const dir = path.join(work, "native-to-web-dir");
  const src = path.join(dir, "bundle");
  const zipFile = path.join(dir, "bundle.zip");
  writeFile(path.join(src, "a.txt"), `alpha-${Date.now()}\n`);
  writeFile(path.join(src, "nested", "b.txt"), `beta-${Date.now()}\n`);
  writeFile(path.join(src, ".gitignore"), "ignored.txt\n");
  writeFile(path.join(src, "ignored.txt"), "must not be transferred\n");
  fs.mkdirSync(path.join(src, "empty"), { recursive: true });
  fs.symlinkSync("a.txt", path.join(src, "a-link"));

  const send = spawnFileSend(src, ["--symlinks", "preserve"]);
  const code = await waitForPairingCode(send, "directory sender");

  const { page, logs, done } = await startWebReceiver(browser, code);
  await done;
  await page.waitForFunction(() => Array.from(document.querySelectorAll("#downloads a")).some((a) => a.textContent === "Download all as ZIP"), null, { timeout: 5000 });
  await saveDownload(page, () => page.getByText("Download all as ZIP", { exact: true }).click(), zipFile);
  const sendCode = await waitProc(send.proc);
  const { stdout, stderr } = send.output();
  assertEqual(sendCode, 0, `directory send exited non-zero\nstdout=${stdout}\nstderr=${stderr}`);
  const entries = readZipEntries(zipFile);
  const modes = readZipModes(zipFile);
  assertEqual(entries.get("bundle/a.txt")?.toString("utf8"), fs.readFileSync(path.join(src, "a.txt"), "utf8"), "zip bundle/a.txt mismatch");
  assertEqual(entries.get("bundle/nested/b.txt")?.toString("utf8"), fs.readFileSync(path.join(src, "nested", "b.txt"), "utf8"), "zip bundle/nested/b.txt mismatch");
  assertEqual(entries.get("bundle/a-link")?.toString("utf8"), "a.txt", "zip symlink target mismatch");
  if (entries.has("bundle/ignored.txt")) throw new Error("gitignored file was included in browser ZIP");
  if (!stdout.includes("1 ignored")) throw new Error(`sender preflight did not report ignored path\nstdout=${stdout}`);
  if (!entries.has("bundle/empty/")) throw new Error("zip empty directory missing");
  if ((modes.get("bundle/a-link") & 0xf000) !== 0xa000) throw new Error(`zip symlink mode mismatch: ${modes.get("bundle/a-link")?.toString(8)}`);
  if ((modes.get("bundle/empty/") & 0xf000) !== 0x4000) throw new Error(`zip directory mode mismatch: ${modes.get("bundle/empty/")?.toString(8)}`);
  if (logs.length) throw new Error(`native-dir->web browser logs:\n${logs.join("\n")}`);
  console.log("ok native->web directory zip");
}

async function webFolderToNative(browser) {
  console.log("start web folder->native");
  const dir = path.join(work, "web-folder-to-native");
  const src = path.join(dir, "webfolder");
  const out = path.join(dir, "out");
  writeFile(path.join(src, "a.txt"), `folder-alpha-${Date.now()}\n`);
  writeFile(path.join(src, "nested", "b.txt"), `folder-beta-${Date.now()}\n`);
  fs.mkdirSync(out, { recursive: true });
  const { logs } = await transferWebInputToNative(browser, {
    src,
    out,
    label: "web folder->native",
    inputSelector: "#folderInput",
    sendSelector: "",
  });
  assertEqual(sha256(path.join(out, "webfolder", "a.txt")), sha256(path.join(src, "a.txt")), "web folder a.txt mismatch");
  assertEqual(sha256(path.join(out, "webfolder", "nested", "b.txt")), sha256(path.join(src, "nested", "b.txt")), "web folder nested/b.txt mismatch");
  if (logs.length) throw new Error(`web-folder->native browser logs:\n${logs.join("\n")}`);
  console.log("ok web folder->native");
}

(async () => {
  const browserType = { chromium, firefox, webkit }[browserName];
  if (!browserType) throw new Error(`unsupported Playwright browser ${browserName}`);
  console.log(`launching ${browserName} channel=${channel || "bundled"}`);
  const launchOptions = { headless: true };
  if (browserName === "chromium") {
    launchOptions.args = ["--disable-breakpad", "--disable-crash-reporter", ...browserArgs];
  }
  if (channel) launchOptions.channel = channel;
  const browser = await browserType.launch(launchOptions);
  console.log("browser launched");
  try {
    await runSmoke(browser, "web protocol guards", 15000, webProtocolGuards);
    await runSmoke(browser, "native->web file", 45000, nativeToWebFile);
    await runSmoke(browser, "browser persistent receive resume", 90000, browserPersistentReceiveResume);
    await runSmoke(browser, "browser corrupt persistent resume", 60000, browserCorruptPersistentResume);
    await runSmoke(browser, "native->web text", 45000, nativeToWebText);
    await runSmoke(browser, "web->native file", 45000, webToNativeFile);
    await runSmoke(browser, "web->native completed file", 45000, webToNativeCompletedFile);
    await runSmoke(browser, "web->native skip file", 45000, webToNativeSkipFile);
    await runSmoke(browser, "web->native text", 45000, webToNativeText);
    await runSmoke(browser, "web->web file", 45000, webToWebFile);
    await runSmoke(browser, "web->web text", 45000, webToWebText);
    await runSmoke(browser, "web->web parallel lane fallback", 45000, webToWebParallelLaneFallback);
    await runSmoke(browser, "web->web unordered reorder", 45000, webToWebUnorderedReorder);
    await runSmoke(browser, "web->web direct failure fallback", 30000, webToWebFallback);
    await runSmoke(browser, "native host->web note", 45000, nativeHostToWebNote);
    await runSmoke(browser, "web host->native note", 45000, webHostToNativeNote);
    await runSmoke(browser, "web->web note", 45000, webToWebNote);
    await runSmoke(browser, "web persistent note isolation", 20000, webPersistentNoteIsolation);
    await runSmoke(browser, "web error handling", 10000, webErrorHandling);
    await runSmoke(browser, "web cancel handling", 10000, webCancelHandling);
    await runSmoke(browser, "web->native resume file", 45000, webToNativeResumeFile);
    await runSmoke(browser, "web->native corrupt resume file", 45000, webToNativeCorruptResumeFile);
    await runSmoke(browser, "native->web directory zip", 45000, nativeToWebDirectory);
    await runSmoke(browser, "web folder->native", 45000, webFolderToNative);
  } finally {
    await closeSmokeBrowser(browser);
  }
})().then(
  () => process.exit(0),
  (err) => {
    console.error(err);
    process.exit(1);
  },
);
NODE

echo "all native/web smoke checks passed"
