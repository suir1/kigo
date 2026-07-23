#!/usr/bin/env node

const assert = require("node:assert/strict");
const test = require("node:test");
const {
  assertRelayProof,
  parseArgs,
  sanitize,
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
