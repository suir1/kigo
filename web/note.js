const NOTE_DEFAULT_PAD = "main";
const NOTE_MAX_TEXT_BYTES = 1 << 20;
const NOTE_MAX_PAD_BYTES = 64;
const NOTE_DEBOUNCE_MS = 250;
const NOTE_DRAFT_STORAGE_PREFIX = "kigo-note-draft-v1:";
const NOTE_DRAFT_TTL_MS = 7 * 24 * 60 * 60 * 1000;
const NOTE_DRAFT_MAX_ENTRIES = 3;
const NOTE_PERSISTENT_PROTOCOL_VERSION = 1;
const NOTE_PERSISTENT_RECORD_VERSION = 1;
const NOTE_PERSISTENT_KEY_INFO = "kigo-note-store-v1";
const NOTE_PERSISTENT_RECONNECT_ATTEMPTS = 3;
const NOTE_RECENT_STORAGE_KEY = "kigo-note-recent-v1";
const NOTE_RECENT_STORAGE_VERSION = 1;
const NOTE_RECENT_MAX_ENTRIES = 20;

const noteCodeEl = document.querySelector("#noteCode");
const notePadEl = document.querySelector("#notePad");
const noteEditorEl = document.querySelector("#noteEditor");
const noteStateEl = document.querySelector("#noteState");
const hostNoteButton = document.querySelector("#hostNote");
const joinNoteButton = document.querySelector("#joinNote");
const clearNoteButton = document.querySelector("#clearNote");
const leaveNoteButton = document.querySelector("#leaveNote");
const noteRecentsEl = document.querySelector("#noteRecents");

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
  noteRecentsEl.querySelectorAll("[data-note-recent-open]").forEach((button) => {
    button.disabled = busy;
  });
};
window.syncNoteControls();
renderBrowserNoteRecents();

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
  noteStateEl.textContent = "Opening";
  if (host) showNoteShare(code, pad);
  log(`Opening persistent notepad ${code}, pad ${pad}...`);

  const draft = await createBrowserNoteDraftStore(code, host ? "host" : "join", pad);
  const restoredDraft = await draft.load();
  const workspace = { document: restoredDraft || emptyNoteDocument(pad), draft };
  noteEditorEl.value = workspace.document.text;
  if (restoredDraft) {
    noteStateEl.textContent = `Recovered draft revision ${restoredDraft.revision}`;
    log(`Recovered encrypted draft revision ${restoredDraft.revision}.`);
  }
  try {
    for (let attempt = 1; attempt <= NOTE_PERSISTENT_RECONNECT_ATTEMPTS; attempt++) {
      try {
        const connection = await openPersistentBrowserNote(code, pad, task);
        await syncPersistentBrowserNote(connection, workspace, code, pad);
        touchBrowserNoteRecent(code, pad);
        renderBrowserNoteRecents();
        log(attempt === 1 ? "Encrypted persistent notepad available." : "Encrypted persistent notepad reconnected.");
        const result = await runPersistentBrowserNoteEditor(connection, workspace, code, pad, task);
        if (result === "leave") return;
        throw new Error("persistent notepad connection closed");
      } catch (err) {
        if (task.canceled || attempt >= NOTE_PERSISTENT_RECONNECT_ATTEMPTS) throw err;
        setNoteConnected(false);
        noteStateEl.textContent = `Reconnecting ${attempt + 1}/${NOTE_PERSISTENT_RECONNECT_ATTEMPTS}`;
        log(`Notepad connection interrupted. Reconnecting ${attempt + 1}/${NOTE_PERSISTENT_RECONNECT_ATTEMPTS}...`);
        await new Promise((resolve) => setTimeout(resolve, 1000));
      }
    }
  } finally {
    setNoteConnected(false);
    activeNoteClear = null;
    activeNoteLeave = null;
  }
}

function browserNoteRecentSort(entries) {
  entries.sort((left, right) => {
    if (left.favorite !== right.favorite) return left.favorite ? -1 : 1;
    if (left.last_opened !== right.last_opened) return right.last_opened - left.last_opened;
    const codeOrder = left.code.localeCompare(right.code);
    return codeOrder || left.pad.localeCompare(right.pad);
  });
  return entries;
}

function loadBrowserNoteRecents() {
  try {
    const raw = localStorage.getItem(NOTE_RECENT_STORAGE_KEY);
    if (!raw) return [];
    const stored = JSON.parse(raw);
    if (stored?.version !== NOTE_RECENT_STORAGE_VERSION || !Array.isArray(stored.entries)) {
      throw new Error("invalid recent notepad catalog");
    }
    if (stored.entries.length > NOTE_RECENT_MAX_ENTRIES) {
      throw new Error("recent notepad catalog exceeds its item limit");
    }
    const seen = new Set();
    const entries = stored.entries.map((entry) => {
      const code = normalizeCode(typeof entry?.code === "string" ? entry.code : "");
      const pad = normalizeNotePad(typeof entry?.pad === "string" ? entry.pad : "");
      if (!validPairingCode(code)) throw new Error("invalid recent notepad code");
      validateNotePad(pad);
      if (!Number.isSafeInteger(entry.last_opened) || entry.last_opened <= 0) {
        throw new Error("invalid recent notepad timestamp");
      }
      const identity = code + "\0" + pad;
      if (seen.has(identity)) throw new Error("duplicate recent notepad");
      seen.add(identity);
      return { code, pad, favorite: entry.favorite === true, last_opened: entry.last_opened };
    });
    return browserNoteRecentSort(entries).slice(0, NOTE_RECENT_MAX_ENTRIES);
  } catch {
    try { localStorage.removeItem(NOTE_RECENT_STORAGE_KEY); } catch {}
    return [];
  }
}

function saveBrowserNoteRecents(entries) {
  try {
    browserNoteRecentSort(entries);
    localStorage.setItem(NOTE_RECENT_STORAGE_KEY, JSON.stringify({
      version: NOTE_RECENT_STORAGE_VERSION,
      entries: entries.slice(0, NOTE_RECENT_MAX_ENTRIES),
    }));
    return true;
  } catch {
    return false;
  }
}

function touchBrowserNoteRecent(code, pad) {
  code = normalizeCode(code);
  pad = normalizeNotePad(pad);
  const entries = loadBrowserNoteRecents();
  const existing = entries.find((entry) => entry.code === code && entry.pad === pad);
  const kept = entries.filter((entry) => entry.code !== code || entry.pad !== pad);
  kept.push({ code, pad, favorite: existing?.favorite === true, last_opened: Date.now() });
  saveBrowserNoteRecents(kept);
}

function setBrowserNoteRecentFavorite(code, pad, favorite) {
  const entries = loadBrowserNoteRecents();
  const entry = entries.find((candidate) => candidate.code === code && candidate.pad === pad);
  if (!entry) return;
  entry.favorite = favorite;
  saveBrowserNoteRecents(entries);
}

function removeBrowserNoteRecent(code, pad) {
  saveBrowserNoteRecents(loadBrowserNoteRecents().filter((entry) => entry.code !== code || entry.pad !== pad));
}

function renderBrowserNoteRecents() {
  const entries = loadBrowserNoteRecents();
  noteRecentsEl.replaceChildren();
  if (!entries.length) {
    const empty = document.createElement("span");
    empty.className = "note-recents-empty";
    empty.textContent = "No recent notepads.";
    noteRecentsEl.append(empty);
    return;
  }
  entries.forEach((entry) => {
    const row = document.createElement("div");
    row.className = "note-recent-row";
    const identity = document.createElement("div");
    identity.className = "note-recent-identity";
    const code = document.createElement("strong");
    code.className = "note-recent-code";
    code.textContent = entry.code;
    const pad = document.createElement("span");
    pad.className = "note-recent-pad";
    pad.textContent = entry.pad;
    identity.append(code, pad);
    const favoriteLabel = document.createElement("label");
    const favorite = document.createElement("input");
    favorite.type = "checkbox";
    favorite.checked = entry.favorite;
    favorite.addEventListener("change", () => {
      setBrowserNoteRecentFavorite(entry.code, entry.pad, favorite.checked);
      renderBrowserNoteRecents();
    });
    favoriteLabel.append(favorite, document.createTextNode("Favorite"));
    const open = document.createElement("button");
    open.type = "button";
    open.dataset.noteRecentOpen = "";
    open.textContent = "Open";
    open.disabled = busy;
    open.addEventListener("click", () => {
      noteCodeEl.value = entry.code;
      notePadEl.value = entry.pad;
      runTask(async (task) => runBrowserNote("join", entry.code, entry.pad, task));
    });
    const remove = document.createElement("button");
    remove.type = "button";
    remove.textContent = "Remove";
    remove.addEventListener("click", () => {
      removeBrowserNoteRecent(entry.code, entry.pad);
      renderBrowserNoteRecents();
    });
    row.append(identity, favoriteLabel, open, remove);
    noteRecentsEl.append(row);
  });
}

async function openPersistentBrowserNote(code, pad, task) {
  const token = await roomToken(code);
  const padToken = await persistentBrowserNotePadToken(pad);
  const scheme = location.protocol === "https:" ? "wss:" : "ws:";
  const socket = new WebSocket(`${scheme}//${location.host}/api/note-sync/${token}/${padToken}`);
  const removeCleanup = task.addCleanup(() => socket.close());
  try {
    await new Promise((resolve, reject) => {
      const timeout = setTimeout(() => reject(new Error("persistent notepad connection timed out")), 10000);
      socket.addEventListener("open", () => {
        clearTimeout(timeout);
        resolve();
      }, { once: true });
      socket.addEventListener("error", () => {
        clearTimeout(timeout);
        reject(new Error("persistent notepad connection failed"));
      }, { once: true });
    });
    const inbox = createPersistentBrowserNoteInbox(socket);
    const initial = await nextPersistentBrowserNoteMessage(inbox);
    return { socket, inbox, generation: initial.generation, initial, removeCleanup };
  } catch (err) {
    removeCleanup();
    socket.close();
    throw err;
  }
}

async function syncPersistentBrowserNote(connection, workspace, code, pad) {
  const remote = await persistentBrowserNoteDocument(connection.initial, code, pad);
  connection.initial = null;
  if (remote && compareNoteDocuments(remote, workspace.document) > 0) {
    workspace.document = remote;
    noteEditorEl.value = remote.text;
    await workspace.draft.save(remote);
  } else if (workspace.document.revision > 0 && (!remote || compareNoteDocuments(workspace.document, remote) > 0)) {
    await putPersistentBrowserNote(connection, code, workspace.document);
  }
}

async function runPersistentBrowserNoteEditor(connection, workspace, code, pad, task) {
  let documentState = workspace.document;
  let debounceTimer = null;
  let finishLocal;
  let failLocal;
  let sendPending = Promise.resolve();
  const localDone = new Promise((resolve, reject) => {
    finishLocal = resolve;
    failLocal = reject;
  });
  const removeCancel = task.addCleanup(() => finishLocal("canceled"));
  const publish = () => {
    debounceTimer = null;
    const current = documentState;
    sendPending = sendPending.then(() => putPersistentBrowserNote(connection, code, current));
    sendPending.catch(failLocal);
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
    debounceTimer = setTimeout(publish, NOTE_DEBOUNCE_MS);
    noteStateEl.textContent = `Editing revision ${documentState.revision}`;
  };
  const clear = () => {
    clearTimeout(debounceTimer);
    noteEditorEl.value = "";
    onInput();
    clearTimeout(debounceTimer);
    publish();
  };
  const leave = () => {
    if (debounceTimer !== null) {
      clearTimeout(debounceTimer);
      publish();
    }
    sendPending.then(() => finishLocal("leave"), failLocal);
  };

  noteEditorEl.addEventListener("input", onInput);
  activeNoteClear = clear;
  activeNoteLeave = leave;
  setNoteConnected(true);
  noteStateEl.textContent = `Available: ${pad}`;

  const receiveLoop = (async () => {
    for (;;) {
      const message = await nextPersistentBrowserNoteMessage(connection.inbox);
      if (message.generation < connection.generation) continue;
      connection.generation = message.generation;
      const incoming = await persistentBrowserNoteDocument(message, code, pad);
      if (!incoming) {
        if (documentState.revision > 0) await putPersistentBrowserNote(connection, code, documentState);
        continue;
      }
      const order = compareNoteDocuments(incoming, documentState);
      if (order > 0) {
        clearTimeout(debounceTimer);
        debounceTimer = null;
        documentState = incoming;
        workspace.document = incoming;
        await workspace.draft.save(incoming);
        noteEditorEl.value = incoming.text;
        noteStateEl.textContent = `Remote revision ${incoming.revision}`;
      } else if (order < 0) {
        await putPersistentBrowserNote(connection, code, documentState);
      } else {
        noteStateEl.textContent = `Synced revision ${incoming.revision}`;
      }
    }
  })();

  try {
    return await Promise.race([receiveLoop, localDone]);
  } finally {
    removeCancel();
    connection.removeCleanup();
    clearTimeout(debounceTimer);
    noteEditorEl.removeEventListener("input", onInput);
    connection.socket.close();
    await sendPending.catch(() => {});
    await workspace.draft.flush();
  }
}

async function putPersistentBrowserNote(connection, code, document) {
  if (connection.socket.readyState !== WebSocket.OPEN) throw new Error("persistent notepad connection closed");
  const record = await sealPersistentBrowserNote(code, document);
  connection.socket.send(JSON.stringify({
    type: "put",
    version: NOTE_PERSISTENT_PROTOCOL_VERSION,
    base_generation: connection.generation,
    record,
  }));
}

function createPersistentBrowserNoteInbox(socket) {
  const messages = [];
  const waiters = [];
  let failure = null;
  const deliver = (entry) => {
    const waiter = waiters.shift();
    if (waiter) waiter(entry);
    else messages.push(entry);
  };
  socket.addEventListener("message", (event) => {
    try {
      deliver({ message: JSON.parse(event.data) });
    } catch (err) {
      deliver({ error: err });
    }
  });
  socket.addEventListener("close", () => {
    failure = new Error("persistent notepad connection closed");
    while (waiters.length) waiters.shift()({ error: failure });
  });
  socket.addEventListener("error", () => {
    failure = new Error("persistent notepad connection failed");
    while (waiters.length) waiters.shift()({ error: failure });
  });
  return {
    async next() {
      const entry = messages.length ? messages.shift() : failure
        ? { error: failure }
        : await new Promise((resolve) => waiters.push(resolve));
      if (entry.error) throw entry.error;
      return entry.message;
    },
  };
}

async function nextPersistentBrowserNoteMessage(inbox) {
  const message = await inbox.next();
  if (message?.version !== NOTE_PERSISTENT_PROTOCOL_VERSION) {
    throw new Error(`unsupported persistent note protocol version ${message?.version}`);
  }
  if (message.type === "error") throw new Error(message.error || "persistent notepad error");
  if (message.type !== "state" || !Number.isSafeInteger(message.generation) || message.generation < 0) {
    throw new Error("invalid persistent notepad state");
  }
  return message;
}

async function persistentBrowserNoteDocument(message, code, pad) {
  if (!message.record) {
    if (message.generation !== 0) throw new Error("persistent notepad state is missing its record");
    return null;
  }
  const record = message.record;
  if (record.version !== NOTE_PERSISTENT_RECORD_VERSION) {
    throw new Error(`unsupported persistent note record version ${record.version}`);
  }
  const salt = globalThis.KigoSecure.base64ToBytes(record.salt);
  const nonce = globalThis.KigoSecure.base64ToBytes(record.nonce);
  const ciphertext = globalThis.KigoSecure.base64ToBytes(record.ciphertext);
  if (salt.length !== 16 || nonce.length !== 12 || ciphertext.length < 16) {
    throw new Error("invalid persistent notepad encryption parameters");
  }
  const key = await persistentBrowserNoteKey(code, salt);
  const plaintext = await crypto.subtle.decrypt({
    name: "AES-GCM",
    iv: nonce,
    additionalData: persistentBrowserNoteAAD(pad),
    tagLength: 128,
  }, key, ciphertext);
  const document = JSON.parse(new TextDecoder().decode(plaintext));
  validateNoteFrame(noteFrame(document.text === "" ? "clear" : "update", document));
  if (normalizeNotePad(document.pad) !== pad) throw new Error("persistent notepad pad mismatch");
  return noteDocument(document);
}

async function sealPersistentBrowserNote(code, document) {
  validateNoteFrame(noteFrame(document.text === "" ? "clear" : "update", document));
  const salt = crypto.getRandomValues(new Uint8Array(16));
  const nonce = crypto.getRandomValues(new Uint8Array(12));
  const key = await persistentBrowserNoteKey(code, salt);
  const ciphertext = await crypto.subtle.encrypt({
    name: "AES-GCM",
    iv: nonce,
    additionalData: persistentBrowserNoteAAD(document.pad),
    tagLength: 128,
  }, key, new TextEncoder().encode(JSON.stringify(document)));
  return {
    version: NOTE_PERSISTENT_RECORD_VERSION,
    salt: globalThis.KigoSecure.bytesToBase64(salt),
    nonce: globalThis.KigoSecure.bytesToBase64(nonce),
    ciphertext: globalThis.KigoSecure.bytesToBase64(new Uint8Array(ciphertext)),
  };
}

async function persistentBrowserNoteKey(code, salt) {
  const baseKey = await crypto.subtle.importKey(
    "raw", new TextEncoder().encode(normalizeCode(code)), "HKDF", false, ["deriveKey"],
  );
  return crypto.subtle.deriveKey({
    name: "HKDF",
    hash: "SHA-256",
    salt,
    info: new TextEncoder().encode(NOTE_PERSISTENT_KEY_INFO),
  }, baseKey, { name: "AES-GCM", length: 128 }, false, ["encrypt", "decrypt"]);
}

function persistentBrowserNoteAAD(pad) {
  return new TextEncoder().encode(`${NOTE_PERSISTENT_KEY_INFO}\0${normalizeNotePad(pad)}`);
}

async function persistentBrowserNotePadToken(pad) {
  const digest = new Uint8Array(await crypto.subtle.digest(
    "SHA-256", new TextEncoder().encode(`kigo-note-pad:${normalizeNotePad(pad)}`),
  ));
  return [...digest].map((value) => value.toString(16).padStart(2, "0")).join("");
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
