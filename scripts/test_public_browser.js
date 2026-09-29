#!/usr/bin/env node

const assert = require("node:assert/strict");
const test = require("node:test");
const {
  assertLargeFileStorage,
  assertRelayProof,
  extractResumeEvidence,
  parseArgs,
  sanitize,
  summarizeTransferMetrics,
  validateOptions,
} = require("./smoke_public_browser.js");

function validRaw(overrides = {}) {
  return {
    url: "https://kigo.example",
    engine: "firefox",
    channel: "",
    force_turn: "1",
    ignore_tls_errors: "0",
    scenarios: "text,file",
    timeout_seconds: "60",
    artifact_dir: "/tmp/kigo-public-browser-test",
    ...overrides,
  };
}

test("parseArgs maps flags and dry-run", () => {
  assert.deepEqual(parseArgs(["--url", "https://kigo.example", "--dry-run"]), {
    url: "https://kigo.example",
    dryRun: true,
  });
});

test("validateOptions accepts HTTPS and loopback HTTP", () => {
  assert.equal(validateOptions(validRaw()).url, "https://kigo.example");
  assert.equal(
    validateOptions(validRaw({ url: "http://127.0.0.1:8080/" })).url,
    "http://127.0.0.1:8080",
  );
});

test("validateOptions accepts resume only with a checkpoint-sized payload", () => {
  const previous = process.env.KIGO_PUBLIC_BROWSER_FILE_BYTES;
  try {
    process.env.KIGO_PUBLIC_BROWSER_FILE_BYTES = String(8 * 1024 * 1024);
    assert.deepEqual(validateOptions(validRaw({ scenarios: "resume" })).scenarios, ["resume"]);
    process.env.KIGO_PUBLIC_BROWSER_FILE_BYTES = String(4 * 1024 * 1024);
    assert.throws(() => validateOptions(validRaw({ scenarios: "resume" })), /greater than 4194304/);
  } finally {
    if (previous === undefined) delete process.env.KIGO_PUBLIC_BROWSER_FILE_BYTES;
    else process.env.KIGO_PUBLIC_BROWSER_FILE_BYTES = previous;
  }
});

test("validateOptions rejects insecure or credentialed endpoints", () => {
  assert.throws(() => validateOptions(validRaw({ url: "http://kigo.example" })), /must use HTTPS/);
  assert.throws(() => validateOptions(validRaw({ url: "https://user:pass@kigo.example" })), /credentials/);
  assert.throws(() => validateOptions(validRaw({ url: "https://kigo.example/?token=secret" })), /query/);
});

test("validateOptions requires an explicit TLS-error boolean", () => {
  assert.equal(validateOptions(validRaw({ ignore_tls_errors: "1" })).ignoreTLSErrors, true);
  assert.throws(() => validateOptions(validRaw({ ignore_tls_errors: "yes" })), /ignore TLS errors/);
});

test("sanitize removes pairing and room identifiers", () => {
  const code = "K7M9Q2";
  const token = "a".repeat(64);
  const text = sanitize(`failed ${code} at #c=${code} room ${token}`, code);
  assert.equal(text.includes(code), false);
  assert.equal(text.includes(token), false);
  assert.match(text, /REDACTED_CODE/);
  assert.match(text, /REDACTED_ROOM_TOKEN/);
});

test("relay proof requires selected local relay candidate", () => {
  assert.doesNotThrow(() => assertRelayProof([
    { local_candidate_type: "relay", remote_candidate_type: "relay", protocol: "udp" },
  ], true));
  assert.throws(() => assertRelayProof([
    { local_candidate_type: "host", remote_candidate_type: "relay", protocol: "udp" },
  ], true), /local relay/);
  assert.throws(() => assertRelayProof([], false), /not captured/);
});

test("transfer metrics retain only bounded performance and storage evidence", () => {
  assert.deepEqual(summarizeTransferMetrics({
    role: "receiver",
    payloadBytes: 167772177,
    payloadMiBPerSecond: 1.23456,
    rtcMiBPerSecond: 1.34567,
    route: "direct P2P (UDP; srflx/srflx)",
    localCandidateType: "srflx",
    remoteCandidateType: "srflx",
    protocol: "UDP",
    pathCount: 1,
    rttMs: 12.3456,
    maxBufferedBytes: 4194304,
    sendWaitMs: 123.4567,
    storage: {
      type: "opfs",
      mode: "sync-worker",
      writeMs: 456.7891,
      queueWaitMs: 23.4567,
      maxQueuedBytes: 4259840,
      checkpointMs: 78.9123,
    },
    secret: "must not be copied",
  }), {
    role: "receiver",
    payload_bytes: 167772177,
    payload_mib_per_second: 1.235,
    rtc_mib_per_second: 1.346,
    route: "direct P2P (UDP; srflx/srflx)",
    local_candidate_type: "srflx",
    remote_candidate_type: "srflx",
    protocol: "UDP",
    path_count: 1,
    rtt_ms: 12.346,
    max_buffered_bytes: 4194304,
    send_wait_ms: 123.457,
    storage: {
      type: "opfs",
      mode: "sync-worker",
      write_ms: 456.789,
      queue_wait_ms: 23.457,
      max_queued_bytes: 4259840,
      checkpoint_ms: 78.912,
    },
  });
});

test("large Chromium storage proof requires OPFS without memory recovery", () => {
  const large = { engine: "chromium", fileBytes: 128 * 1024 * 1024 + 1 };
  assert.equal(assertLargeFileStorage(large, { storage: { type: "opfs" } }, "Transfer complete."), true);
  assert.equal(assertLargeFileStorage({ ...large, fileBytes: 1024 }, null, ""), false);
  assert.equal(assertLargeFileStorage({ ...large, engine: "firefox" }, null, ""), false);
  assert.throws(() => assertLargeFileStorage(large, { storage: { type: "memory" } }, ""), /did not use OPFS/);
  assert.throws(() => assertLargeFileStorage(large, { storage: { type: "opfs" } }, "using memory recovery"), /memory recovery/);
});

test("resume evidence requires the saved and accepted nonzero offsets to match", () => {
  const fileBytes = 12 * 1024 * 1024;
  assert.deepEqual(extractResumeEvidence(
    `Found saved partial for payload.bin: 4194304/${fileBytes} bytes.\n`
      + "Sender accepted payload.bin resume at 4194304 bytes.",
    fileBytes,
  ), {
    checkpoint_bytes: 4194304,
    saved_partial_bytes: 4194304,
    accepted_offset_bytes: 4194304,
  });
  assert.throws(() => extractResumeEvidence(
    `Found saved partial for payload.bin: 4194304/${fileBytes} bytes.\n`
      + "Sender accepted payload.bin resume at 0 bytes.",
    fileBytes,
  ), /accepted 0 bytes/);
});
