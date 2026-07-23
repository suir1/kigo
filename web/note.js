const NOTE_PROTOCOL = "note";
const NOTE_DEFAULT_PAD = "main";
const NOTE_MAX_TEXT_BYTES = 1 << 20;
const NOTE_MAX_PAD_BYTES = 64;
const NOTE_DEBOUNCE_MS = 250;
const NOTE_DRAFT_STORAGE_PREFIX = "kigo-note-draft-v1:";
const NOTE_DRAFT_TTL_MS = 7 * 24 * 60 * 60 * 1000;
const NOTE_DRAFT_MAX_ENTRIES = 3;

const noteCodeEl = document.querySelector("#noteCode");
const notePadEl = document.querySelector("#notePad");
const noteEditorEl = document.querySelector("#noteEditor");
const noteStateEl = document.querySelector("#noteState");
const hostNoteButton = document.querySelector("#hostNote");
const joinNoteButton = document.querySelector("#joinNote");
const clearNoteButton = document.querySelector("#clearNote");
const leaveNoteButton = document.querySelector("#leaveNote");

let noteConnected = false;
let activeNoteClear = null;
let activeNoteLeave = null;

window.syncNoteControls = function syncNoteControls() {
  hostNoteButton.disabled = busy;
  joinNoteButton.disabled = busy;
  noteCodeEl.disabled = busy;
  notePadEl.disabled = busy;
  noteEditorEl.disabled = !noteConnected;
  clearNoteButton.disabled = !noteConnected;
  leaveNoteButton.disabled = !noteConnected;
};
window.syncNoteControls();

hostNoteButton.addEventListener("click", () => runTask(async (task) => {
  await runBrowserNote("host", noteCodeEl.value, notePadEl.value, task);
}));

joinNoteButton.addEventListener("click", () => runTask(async (task) => {
  const code = normalizeCode(noteCodeEl.value);
  if (!validPairingCode(code)) {
    log("Enter a six-character pairing code.");
    return;
  }
  await runBrowserNote("join", code, notePadEl.value, task);
}));

clearNoteButton.addEventListener("click", () => {
  if (activeNoteClear) activeNoteClear();
});

leaveNoteButton.addEventListener("click", () => {
  if (activeNoteLeave) activeNoteLeave();
});

const noteHashParams = new URLSearchParams(location.hash.slice(1));
const noteHashCode = noteHashParams.get("n");
if (noteHashCode) {
  noteCodeEl.value = normalizeCode(noteHashCode);
  notePadEl.value = normalizeNotePad(noteHashParams.get("p"));
  document.querySelector('[data-tab="note"]').click();
  queueMicrotask(() => joinNoteButton.click());
}

async function runBrowserNote(mode, requestedCode, requestedPad, task) {
  resetOutput();
  const host = mode === "host";
  const normalizedRequest = normalizeCode(requestedCode);
  const code = host ? (normalizedRequest || generateCode()) : normalizedRequest;
  if (!validPairingCode(code)) throw new Error("invalid note pairing code");
  const pad = normalizeNotePad(requestedPad);
  validateNotePad(pad);
  noteCodeEl.value = code;
  notePadEl.value = pad;
  noteEditorEl.value = "";
  noteStateEl.textContent = host ? "Waiting for peer" : "Joining";
  if (host) showNoteShare(code, pad);
  log(host ? `Waiting for notepad peer with code ${code}, pad ${pad}...` : `Joining notepad ${code}, pad ${pad}...`);

  const token = await roomToken(code);
  const role = host ? "sender" : "receiver";
  const draft = await createBrowserNoteDraftStore(code, host ? "host" : "join", pad);
  const restoredDraft = await draft.load();
  const workspace = { document: restoredDraft || emptyNoteDocument(pad), draft };
  noteEditorEl.value = workspace.document.text;
  if (restoredDraft) {
    noteStateEl.textContent = `Recovered draft revision ${restoredDraft.revision}`;
    log(`Recovered encrypted draft revision ${restoredDraft.revision}.`);
  }
  const negotiation = await negotiateBrowserRoute(token, role, task, NOTE_PROTOCOL);
  const directFirst = negotiation?.pair === "web-web";
  if (directFirst) log("Trying LAN host-to-host before STUN and TURN...");

  try {
    await globalThis.kigoWebRTC.runPeerSession({
      role,
      token,
      task,
      protocol: NOTE_PROTOCOL,
      directFirst,
      onRetry: ({ nextAttempt, nextMode, maxAttempts, diagnostics }) => {
        setNoteConnected(false);
        noteStateEl.textContent = `Reconnecting ${nextAttempt}/${maxAttempts}`;
        const route = nextMode === "direct" ? " with STUN" : nextMode === "relay" ? " through TURN" : "";
        const ice = nextMode === "direct" || nextMode === "relay" ? ` (${formatICEDiagnostics(diagnostics)})` : "";
        log(`Notepad connection interrupted${ice}. Reconnecting ${nextAttempt}/${maxAttempts}${route}...`);
      },
    }, async ({ attempt, pipe }) => {
      const session = host
        ? await initNoteHostSession(pipe, code)
        : await initNoteJoinSession(pipe, code);
      await syncBrowserNoteWorkspace(pipe, session, workspace, pad, host);
      log(attempt === 1 ? "Encrypted notepad connected." : "Encrypted notepad reconnected.");
      await runNoteEditor(pipe, session, task, pad, workspace);
      globalThis.kigoWebRTC.clearReconnectToken(token, role, NOTE_PROTOCOL);
    });
  } finally {
    setNoteConnected(false);
    activeNoteClear = null;
    activeNoteLeave = null;
  }
}

async function initNoteHostSession(pipe, code) {
  const senderNonce = globalThis.KigoSecure.randomNonce();
  await pipe.send({
    type: "hello",
    version: VERSION,
    sender_nonce: senderNonce,
    workspace_sync: true,
  });
  const ack = globalThis.KigoSecure.parseJSONFrame(await pipe.recv());
  validateNoteHello(ack, "hello_ack");
  return deriveNoteSession(code, senderNonce, ack.receiver_nonce, true, ack.workspace_sync === true);
}

async function initNoteJoinSession(pipe, code) {
  const hello = globalThis.KigoSecure.parseJSONFrame(await pipe.recv());
  validateNoteHello(hello, "hello");
  const receiverNonce = globalThis.KigoSecure.randomNonce();
  await pipe.send({
    type: "hello_ack",
    version: VERSION,
    receiver_nonce: receiverNonce,
    workspace_sync: hello.workspace_sync === true,
  });
  return deriveNoteSession(code, hello.sender_nonce, receiverNonce, false, hello.workspace_sync === true);
}

function validateNoteHello(message, expectedType) {
  if (!message || message.type !== expectedType) {
    throw new Error(`invalid note hello type ${message?.type}`);
  }
  if (message.version !== VERSION) {
    throw new Error(`unsupported note hello version ${message.version}`);
  }
  if (expectedType === "hello" && !message.sender_nonce) {
    throw new Error("note hello is missing sender nonce");
  }
  if (expectedType === "hello_ack" && !message.receiver_nonce) {
    throw new Error("note hello acknowledgement is missing receiver nonce");
  }
}

async function deriveNoteSession(code, senderNonce, receiverNonce, host, workspaceSync = false) {
  const sendInfo = host ? "kigo-note-v1:host-to-join" : "kigo-note-v1:join-to-host";
  const recvInfo = host ? "kigo-note-v1:join-to-host" : "kigo-note-v1:host-to-join";
  return {
    send: await globalThis.KigoSecure.deriveSessionState(normalizeCode(code), senderNonce, receiverNonce, sendInfo),
    recv: await globalThis.KigoSecure.deriveSessionState(normalizeCode(code), senderNonce, receiverNonce, recvInfo),
    workspaceSync,
  };
}

function createNoteSender(pipe, session) {
  let pending = Promise.resolve();
  return {
    send(frame) {
      const current = pending.then(() => sendNoteEncrypted(pipe, session, frame));
      pending = current.catch(() => {});
      return current;
    },
  };
}

async function sendNoteEncrypted(pipe, session, frame) {
  const normalized = validateNoteFrame({ ...frame, version: VERSION });
  const sequence = session.send.nextSeq + 1;
  session.send.nextSeq = sequence;
  const plaintext = new TextEncoder().encode(JSON.stringify(normalized));
  const ciphertext = await globalThis.KigoSecure.encrypt(session.send, sequence, plaintext);
  await pipe.send({
    version: VERSION,
    seq: sequence,
    ciphertext: globalThis.KigoSecure.bytesToBase64(ciphertext),
  });
}

async function recvNoteEncrypted(pipe, session) {
  const envelope = globalThis.KigoSecure.parseJSONFrame(await pipe.recv());
  if (!envelope || envelope.version !== VERSION) {
    throw new Error(`unsupported note envelope version ${envelope?.version}`);
  }
  const expected = session.recv.nextSeq + 1;
  if (!Number.isSafeInteger(envelope.seq) || envelope.seq !== expected) {
    throw new Error(`unexpected note envelope sequence ${envelope.seq}, want ${expected}`);
  }
  if (typeof envelope.ciphertext !== "string") {
    throw new Error("note envelope is missing ciphertext");
  }
  const ciphertext = globalThis.KigoSecure.base64ToBytes(envelope.ciphertext);
  const plaintext = await globalThis.KigoSecure.decrypt(session.recv, envelope.seq, ciphertext);
  const frame = validateNoteFrame(JSON.parse(new TextDecoder().decode(plaintext)));
  session.recv.nextSeq = envelope.seq;
  return frame;
}

async function syncBrowserNoteWorkspace(pipe, session, workspace, pad, host) {
  if (!session.workspaceSync) return;
  const sender = createNoteSender(pipe, session);
  if (host) {
    await sendBrowserNoteSnapshot(pipe, session, sender, workspace.document);
    await receiveBrowserNoteSnapshot(pipe, session, sender, workspace, pad);
  } else {
    await receiveBrowserNoteSnapshot(pipe, session, sender, workspace, pad);
    await sendBrowserNoteSnapshot(pipe, session, sender, workspace.document);
  }
  noteEditorEl.value = workspace.document.text;
  await workspace.draft.save(workspace.document);
}

async function sendBrowserNoteSnapshot(pipe, session, sender, document) {
  const empty = document.revision === 0;
  await sender.send(empty
    ? { type: "ping", version: VERSION, pad: document.pad }
    : noteFrame(document.text === "" ? "clear" : "update", document));
  const response = await recvNoteEncrypted(pipe, session);
  if (normalizeNotePad(response.pad) !== document.pad) {
    throw new Error(`peer selected note pad ${response.pad}`);
  }
  const expected = empty ? "pong" : "ack";
  if (response.type !== expected) {
    throw new Error(`expected note workspace ${expected}, got ${response.type}`);
  }
}

async function receiveBrowserNoteSnapshot(pipe, session, sender, workspace, pad) {
  const frame = await recvNoteEncrypted(pipe, session);
  if (normalizeNotePad(frame.pad) !== pad) {
    throw new Error(`peer selected note pad ${frame.pad}`);
  }
  if (frame.type === "ping") {
    await sender.send({ type: "pong", version: VERSION, pad });
    return;
  }
  if (frame.type !== "update" && frame.type !== "clear") {
    throw new Error(`expected note workspace snapshot, got ${frame.type}`);
  }
  const incoming = noteDocument(frame);
  if (compareNoteDocuments(incoming, workspace.document) > 0) {
    workspace.document = incoming;
  }
  await sender.send({
    type: "ack",
    version: VERSION,
    pad,
    revision: workspace.document.revision,
    timestamp: workspace.document.timestamp,
  });
}

async function runNoteEditor(pipe, session, task, pad, workspace) {
  const sender = createNoteSender(pipe, session);
  let documentState = workspace.document;
  let debounceTimer = null;
  let finishLocal;
  let failLocal;
  const localDone = new Promise((resolve, reject) => {
    finishLocal = resolve;
    failLocal = reject;
  });
  const removeCancel = task.addCleanup(() => finishLocal("canceled"));

  const publishDocument = (type, document) => {
    sender.send(noteFrame(type, document)).catch(failLocal);
    noteStateEl.textContent = `Sent revision ${document.revision}`;
  };
  const publishPending = () => {
    debounceTimer = null;
    publishDocument("update", documentState);
  };
  const onInput = () => {
    try {
      documentState = updateLocalNote(documentState, noteEditorEl.value);
      workspace.document = documentState;
      workspace.draft.save(documentState);
    } catch (err) {
      failLocal(err);
      return;
    }
    clearTimeout(debounceTimer);
    debounceTimer = setTimeout(publishPending, NOTE_DEBOUNCE_MS);
    noteStateEl.textContent = `Editing revision ${documentState.revision}`;
  };
  const clear = () => {
    clearTimeout(debounceTimer);
    debounceTimer = null;
    noteEditorEl.value = "";
    try {
      documentState = updateLocalNote(documentState, "");
      workspace.document = documentState;
      workspace.draft.save(documentState);
      publishDocument("clear", documentState);
    } catch (err) {
      failLocal(err);
    }
  };
  const leave = () => {
    clearTimeout(debounceTimer);
    debounceTimer = null;
    sender.send({ type: "bye", version: VERSION, pad })
      .then(() => finishLocal("leave"), failLocal);
  };

  noteEditorEl.addEventListener("input", onInput);
  activeNoteClear = clear;
  activeNoteLeave = leave;
  setNoteConnected(true);
  noteStateEl.textContent = `Connected: ${pad}`;

  const receiveLoop = (async () => {
    for (;;) {
      const frame = await recvNoteEncrypted(pipe, session);
      if (normalizeNotePad(frame.pad) !== pad) {
        throw new Error(`peer selected note pad ${frame.pad}`);
      }
      switch (frame.type) {
      case "update":
      case "clear": {
        const incoming = noteDocument(frame);
        if (compareNoteDocuments(incoming, documentState) > 0) {
          clearTimeout(debounceTimer);
          debounceTimer = null;
          documentState = incoming;
          workspace.document = documentState;
          workspace.draft.save(documentState);
          noteEditorEl.value = incoming.text;
          noteStateEl.textContent = `Remote revision ${incoming.revision}`;
        }
        await sender.send({
          type: "ack",
          version: VERSION,
          pad,
          revision: documentState.revision,
          timestamp: documentState.timestamp,
        });
        break;
      }
      case "ack":
        noteStateEl.textContent = `Synced revision ${frame.revision}`;
        break;
      case "ping":
        await sender.send({ type: "pong", version: VERSION, pad });
        break;
      case "pong":
        break;
      case "bye":
        noteStateEl.textContent = "Peer left";
        log("Peer closed the notepad.");
        return "peer-left";
      }
    }
  })();

  try {
    await Promise.race([receiveLoop, localDone]);
  } finally {
    removeCancel();
    clearTimeout(debounceTimer);
    noteEditorEl.removeEventListener("input", onInput);
    await workspace.draft.flush();
  }
}

function validateNoteFrame(frame) {
  if (!frame || frame.version !== VERSION) {
    throw new Error(`unsupported note frame version ${frame?.version}`);
  }
  const type = frame.type;
  if (!["update", "clear", "ack", "ping", "pong", "bye"].includes(type)) {
    throw new Error(`unsupported note frame type ${type}`);
  }
  if (frame.pad !== undefined && frame.pad !== "") validateNotePad(frame.pad);
  if (type === "update" || type === "clear") {
    validateNotePad(frame.pad || NOTE_DEFAULT_PAD);
    validateNoteText(frame.text || "");
    if (!Number.isSafeInteger(frame.revision) || frame.revision <= 0) {
      throw new Error("note document revision must be positive");
    }
    if (!Number.isSafeInteger(frame.timestamp) || frame.timestamp <= 0) {
      throw new Error("note document timestamp must be positive");
    }
  }
  if (type === "ack" && (!Number.isSafeInteger(frame.revision) || frame.revision <= 0)) {
    throw new Error("note ack revision must be positive");
  }
  return frame;
}

function emptyNoteDocument(pad = NOTE_DEFAULT_PAD) {
  return { pad: normalizeNotePad(pad), text: "", revision: 0, timestamp: 0 };
}

async function createBrowserNoteDraftStore(code, role, pad) {
  const key = await browserNoteDraftStorageKey(code, role, pad);
  let pending = Promise.resolve();
  let warned = false;
  const warn = (err) => {
    if (warned) return;
    warned = true;
    log(`Encrypted draft storage unavailable: ${err.message || err}`);
  };
  return {
    async load() {
      try {
        return await loadBrowserNoteDraft(key, code, role, pad);
      } catch (err) {
        warn(err);
        return null;
      }
    },
    save(document) {
      if (!document || document.revision === 0) return pending;
      pending = pending
        .then(() => saveBrowserNoteDraft(key, code, role, pad, document))
        .catch(warn);
      return pending;
    },
    flush() {
      return pending;
    },
  };
}

async function browserNoteDraftStorageKey(code, role, pad) {
  const identity = `${await roomToken(code)}\0${role}\0${pad}`;
  const digest = new Uint8Array(await crypto.subtle.digest(
    "SHA-256",
    new TextEncoder().encode(`kigo-note-draft-key:${identity}`),
  ));
  return NOTE_DRAFT_STORAGE_PREFIX + [...digest].map((value) => value.toString(16).padStart(2, "0")).join("");
}

async function saveBrowserNoteDraft(key, code, role, pad, document) {
  validateNoteFrame(noteFrame(document.text === "" ? "clear" : "update", document));
  const now = Date.now();
  const salt = crypto.getRandomValues(new Uint8Array(16));
  const nonce = crypto.getRandomValues(new Uint8Array(12));
  const aesKey = await browserNoteDraftKey(code, salt);
  const plaintext = new TextEncoder().encode(JSON.stringify(document));
  const ciphertext = new Uint8Array(await crypto.subtle.encrypt({
    name: "AES-GCM",
    iv: nonce,
    additionalData: browserNoteDraftAAD(role, pad),
    tagLength: 128,
  }, aesKey, plaintext));
  const record = JSON.stringify({
    version: 1,
    updated_at: now,
    expires_at: now + NOTE_DRAFT_TTL_MS,
    salt: globalThis.KigoSecure.bytesToBase64(salt),
    nonce: globalThis.KigoSecure.bytesToBase64(nonce),
    ciphertext: globalThis.KigoSecure.bytesToBase64(ciphertext),
  });
  pruneBrowserNoteDrafts(now, NOTE_DRAFT_MAX_ENTRIES - 1, key);
  try {
    localStorage.setItem(key, record);
  } catch (err) {
    pruneBrowserNoteDrafts(now, 0, key);
    localStorage.setItem(key, record);
  }
}

async function loadBrowserNoteDraft(key, code, role, pad) {
  const raw = localStorage.getItem(key);
  if (!raw) return null;
  let record;
  try {
    record = JSON.parse(raw);
    if (record.version !== 1 || !Number.isSafeInteger(record.expires_at) || record.expires_at <= Date.now()) {
      localStorage.removeItem(key);
      return null;
    }
    const salt = globalThis.KigoSecure.base64ToBytes(record.salt);
    const nonce = globalThis.KigoSecure.base64ToBytes(record.nonce);
    if (salt.length !== 16 || nonce.length !== 12) throw new Error("invalid encrypted draft parameters");
    const aesKey = await browserNoteDraftKey(code, salt);
    const plaintext = await crypto.subtle.decrypt({
      name: "AES-GCM",
      iv: nonce,
      additionalData: browserNoteDraftAAD(role, pad),
      tagLength: 128,
    }, aesKey, globalThis.KigoSecure.base64ToBytes(record.ciphertext));
    const document = JSON.parse(new TextDecoder().decode(plaintext));
    validateNoteFrame(noteFrame(document.text === "" ? "clear" : "update", document));
    if (normalizeNotePad(document.pad) !== pad) throw new Error("encrypted draft pad mismatch");
    return noteDocument(document);
  } catch (err) {
    localStorage.removeItem(key);
    throw new Error(`read encrypted note draft: ${err.message || err}`);
  }
}

async function browserNoteDraftKey(code, salt) {
  const baseKey = await crypto.subtle.importKey(
    "raw",
    new TextEncoder().encode(normalizeCode(code)),
    "HKDF",
    false,
    ["deriveKey"],
  );
  return crypto.subtle.deriveKey({
    name: "HKDF",
    hash: "SHA-256",
    salt,
    info: new TextEncoder().encode("kigo-note-draft-v1"),
  }, baseKey, { name: "AES-GCM", length: 128 }, false, ["encrypt", "decrypt"]);
}

function browserNoteDraftAAD(role, pad) {
  return new TextEncoder().encode(`kigo-note-draft-v1\0${role}\0${pad}`);
}

function pruneBrowserNoteDrafts(now, maximum, keepKey) {
  const entries = [];
  for (let index = 0; index < localStorage.length; index++) {
    const key = localStorage.key(index);
    if (!key?.startsWith(NOTE_DRAFT_STORAGE_PREFIX) || key === keepKey) continue;
    try {
      const record = JSON.parse(localStorage.getItem(key));
      if (!Number.isSafeInteger(record.expires_at) || record.expires_at <= now) {
        localStorage.removeItem(key);
        index--;
        continue;
      }
      entries.push({ key, updatedAt: Number(record.updated_at) || 0 });
    } catch {
      localStorage.removeItem(key);
      index--;
    }
  }
  entries.sort((left, right) => left.updatedAt - right.updatedAt);
  while (entries.length > maximum) localStorage.removeItem(entries.shift().key);
}

function updateLocalNote(current, text) {
  validateNoteText(text);
  return {
    pad: current.pad,
    text,
    revision: current.revision + 1,
    timestamp: Math.max(Date.now(), current.timestamp + 1),
  };
}

function noteDocument(frame) {
  return {
    pad: normalizeNotePad(frame.pad),
    text: frame.text || "",
    revision: frame.revision,
    timestamp: frame.timestamp,
  };
}

function noteFrame(type, document) {
  return {
    type,
    version: VERSION,
    pad: document.pad,
    text: document.text,
    revision: document.revision,
    timestamp: document.timestamp,
  };
}

function compareNoteDocuments(left, right) {
  if (left.revision !== right.revision) return left.revision < right.revision ? -1 : 1;
  if (left.timestamp !== right.timestamp) return left.timestamp < right.timestamp ? -1 : 1;
  if (left.text === right.text) return 0;
  return left.text < right.text ? -1 : 1;
}

function normalizeNotePad(pad) {
  const normalized = String(pad || "").trim();
  return normalized || NOTE_DEFAULT_PAD;
}

function validateNotePad(pad) {
  const normalized = normalizeNotePad(pad);
  if (new TextEncoder().encode(normalized).length > NOTE_MAX_PAD_BYTES) {
    throw new Error(`note pad exceeds ${NOTE_MAX_PAD_BYTES} bytes`);
  }
  if (/[\0\r\n]/.test(normalized)) throw new Error("note pad contains unsupported characters");
}

function validateNoteText(text) {
  if (new TextEncoder().encode(text).length > NOTE_MAX_TEXT_BYTES) {
    throw new Error(`note text exceeds ${NOTE_MAX_TEXT_BYTES} bytes`);
  }
}

function showNoteShare(code, pad) {
  const fragment = new URLSearchParams({ n: code });
  if (pad !== NOTE_DEFAULT_PAD) fragment.set("p", pad);
  const link = `${location.origin}/#${fragment}`;
  const codeLabel = document.createElement("strong");
  codeLabel.textContent = "Code:";
  const padLabel = document.createElement("strong");
  padLabel.textContent = "Pad:";
  const linkLabel = document.createElement("strong");
  linkLabel.textContent = "Notepad:";
  const anchor = document.createElement("a");
  anchor.href = link;
  anchor.textContent = link;
  shareEl.replaceChildren(codeLabel, ` ${code}`, document.createElement("br"), padLabel, ` ${pad}`, document.createElement("br"), linkLabel, " ", anchor);
  shareEl.classList.remove("hidden");
}

function setNoteConnected(connected) {
  noteConnected = connected;
  if (!connected && noteStateEl.textContent !== "Peer left") {
    noteStateEl.textContent = "Not connected";
  }
  window.syncNoteControls();
}
