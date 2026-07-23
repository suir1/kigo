(function installKigoSecure(global) {
  "use strict";

  const encoder = new TextEncoder();
  const decoder = new TextDecoder();

  async function deriveSessionState(secretText, senderNonce, receiverNonce, infoText) {
    const secret = encoder.encode(secretText);
    const salt = encoder.encode(`${senderNonce}:${receiverNonce}`);
    const info = encoder.encode(infoText);
    const baseKey = await crypto.subtle.importKey("raw", secret, "HKDF", false, ["deriveBits"]);
    const bits = await crypto.subtle.deriveBits({
      name: "HKDF",
      hash: "SHA-256",
      salt,
      info,
    }, baseKey, 160);
    const material = new Uint8Array(bits);
    const key = await crypto.subtle.importKey(
      "raw",
      material.slice(0, 16),
      "AES-GCM",
      false,
      ["encrypt", "decrypt"],
    );
    return { key, prefix: material.slice(16, 20), nextSeq: 0 };
  }

  async function encrypt(state, sequence, plaintext) {
    return new Uint8Array(await crypto.subtle.encrypt({
      name: "AES-GCM",
      iv: nonce(state.prefix, sequence),
      additionalData: aad(sequence),
      tagLength: 128,
    }, state.key, plaintext));
  }

  async function decrypt(state, sequence, ciphertext) {
    return new Uint8Array(await crypto.subtle.decrypt({
      name: "AES-GCM",
      iv: nonce(state.prefix, sequence),
      additionalData: aad(sequence),
      tagLength: 128,
    }, state.key, ciphertext));
  }

  function nonce(prefix, sequence) {
    const out = new Uint8Array(12);
    out.set(prefix, 0);
    let value = BigInt(sequence);
    for (let index = 11; index >= 4; index--) {
      out[index] = Number(value & 255n);
      value >>= 8n;
    }
    return out;
  }

  function aad(sequence) {
    return encoder.encode(`kigo-v1:${sequence}`);
  }

  function parseJSONFrame(frame) {
    if (typeof frame === "string") return JSON.parse(frame);
    if (frame instanceof ArrayBuffer) return JSON.parse(decoder.decode(new Uint8Array(frame)));
    if (ArrayBuffer.isView(frame)) {
      return JSON.parse(decoder.decode(new Uint8Array(frame.buffer, frame.byteOffset, frame.byteLength)));
    }
    throw new Error("invalid JSON transport frame");
  }

  function randomNonce() {
    const bytes = crypto.getRandomValues(new Uint8Array(16));
    return bytesToBase64(bytes).replaceAll("+", "-").replaceAll("/", "_").replaceAll("=", "");
  }

  function bytesToBase64(bytes) {
    const parts = [];
    const blockSize = 0x8000;
    for (let offset = 0; offset < bytes.length; offset += blockSize) {
      parts.push(String.fromCharCode(...bytes.subarray(offset, offset + blockSize)));
    }
    return btoa(parts.join(""));
  }

  function base64ToBytes(text) {
    const binary = atob(text);
    const out = new Uint8Array(binary.length);
    for (let index = 0; index < binary.length; index++) out[index] = binary.charCodeAt(index);
    return out;
  }

  global.KigoSecure = Object.freeze({
    base64ToBytes,
    bytesToBase64,
    decrypt,
    deriveSessionState,
    encrypt,
    parseJSONFrame,
    randomNonce,
  });
})(globalThis);
