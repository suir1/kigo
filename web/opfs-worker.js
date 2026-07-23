const handles = new Map();

function reply(id, value = {}) {
  self.postMessage({ id, ...value });
}

self.onmessage = async (event) => {
  const message = event.data || {};
  try {
    if (message.type === "init") {
      for (const entry of message.entries || []) {
        if (typeof entry.handle?.createSyncAccessHandle !== "function") {
          throw new Error("OPFS sync access handles are unavailable");
        }
        handles.set(entry.itemID, await entry.handle.createSyncAccessHandle());
      }
      reply(message.id, { type: "ready" });
      return;
    }
    if (message.type === "close") {
      for (const handle of handles.values()) handle.close();
      handles.clear();
      reply(message.id, { type: "close_complete" });
      return;
    }
    const access = handles.get(message.itemID);
    if (!access) throw new Error("OPFS item " + message.itemID + " is not open");
    if (message.type === "write") {
      const data = new Uint8Array(message.data);
      const written = access.write(data, { at: message.offset });
      if (written !== data.length) throw new Error("OPFS short write: " + written + "/" + data.length);
      reply(message.id, { type: "write_complete" });
    } else if (message.type === "flush") {
      access.flush();
      reply(message.id, { type: "flush_complete" });
    } else if (message.type === "truncate") {
      access.truncate(message.size);
      access.flush();
      reply(message.id, { type: "truncate_complete" });
    } else {
      throw new Error("unsupported OPFS worker command " + message.type);
    }
  } catch (error) {
    reply(message.id, { type: "error", error: error?.message || String(error) });
  }
};
