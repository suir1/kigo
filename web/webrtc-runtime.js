(function installKigoWebRTC(global) {
  "use strict";

  const encoder = new TextEncoder();

  function create(options = {}) {
    const signalTimeoutMs = options.signalTimeoutMs || 30 * 1000;
    const directTimeoutMs = options.directTimeoutMs || 5000;
    const reconnectAttempts = options.reconnectAttempts || 3;
    const reconnectDelayMs = options.reconnectDelayMs || 1000;
    const raceDelayMs = options.raceDelayMs ?? 800;
    const directPreferenceMs = options.directPreferenceMs ?? 300;
    const reconnectStoragePrefix = options.reconnectStoragePrefix || "kigo-signal-reconnect-v1:";
    const reconnectProtocol = options.reconnectProtocol || "kigo-reconnect-v1";
    const bufferHighBytes = options.bufferHighBytes || 4 * 1024 * 1024;
    const bufferLowBytes = options.bufferLowBytes || 2 * 1024 * 1024;
    async function createPeer(role, token, task, protocol = "transfer", iceMode = "all", unorderedData = false) {
      if (role !== "sender" && role !== "receiver") throw new Error("invalid WebRTC role");
      const signal = await connectSignal(token, role, protocol);
      const removeSignalCleanup = task.addCleanup(() => signal.close());
      let pc = null;
      let removePeerCleanup = () => {};
      const close = () => {
        removePeerCleanup();
        removeSignalCleanup();
        signal.close();
        if (pc) pc.close();
      };
      try {
        pc = new RTCPeerConnection(await rtcConfig(iceMode));
        removePeerCleanup = task.addCleanup(() => pc.close());
        const candidateTypes = { local: new Set(), remote: new Set() };
        const remoteCandidates = makeRemoteCandidateQueue(pc, candidateTypes.remote);
        signal.on("candidate", remoteCandidates.add);
        pc.onicecandidate = (event) => sendLocalCandidate(signal, event.candidate, candidateTypes.local);
        const result = (dc, dataDC = null) => ({
          pc,
          dc,
          dataDC,
          signal,
          close,
          iceDiagnostics: () => describeICEFailure(pc, candidateTypes),
        });
        if (role === "sender") {
          const dc = pc.createDataChannel("kigo", { ordered: true });
          const dataDC = unorderedData ? pc.createDataChannel("kigo-data", { ordered: false }) : null;
          const offer = await pc.createOffer();
          await pc.setLocalDescription(offer);
          signal.send({ type: "offer", sdp: pc.localDescription.sdp });
          const answer = await signal.wait("answer", "receiver answer");
          await pc.setRemoteDescription({ type: "answer", sdp: answer.sdp });
          await remoteCandidates.flush();
          return result(dc, dataDC);
        }
        let resolveControl;
        let resolveData;
        const dcPromise = new Promise((resolve) => { resolveControl = resolve; });
        const dataDCPromise = unorderedData ? new Promise((resolve) => { resolveData = resolve; }) : null;
        pc.ondatachannel = (event) => {
          if (event.channel.label === "kigo") resolveControl(event.channel);
          else if (unorderedData && event.channel.label === "kigo-data") resolveData(event.channel);
        };
        const offer = await signal.wait("offer", "sender offer");
        await pc.setRemoteDescription({ type: "offer", sdp: offer.sdp });
        await remoteCandidates.flush();
        const answer = await pc.createAnswer();
        await pc.setLocalDescription(answer);
        signal.send({ type: "answer", sdp: pc.localDescription.sdp });
        return result(dcPromise, dataDCPromise);
      } catch (err) {
        close();
        throw err;
      }
    }

    async function connectSignal(token, role, protocol) {
      const ws = new WebSocket(signalURL(token, role, protocol), [reconnectProtocol]);
      const handlers = new Map();
      const waiters = [];
      const backlog = [];
      let closed = false;
      let reconnectSupported = false;
      const failWaiters = (err) => {
        while (waiters.length) waiters.shift().reject(err);
      };
      ws.onmessage = (event) => {
        const msg = JSON.parse(event.data);
        if (msg.type === "error") {
          failWaiters(new Error(msg.error || "signaling error"));
          return;
        }
        if (msg.type === "signal_ready") {
          reconnectSupported = msg.reconnect_supported === true && typeof msg.reconnect_token === "string";
          if (reconnectSupported) storeReconnectToken(token, role, msg.reconnect_token, protocol);
          else clearReconnectToken(token, role, protocol);
          return;
        }
        let handled = false;
        for (const handler of handlers.get(msg.type) || []) {
          handled = true;
          handler(msg);
        }
        const index = waiters.findIndex((waiter) => waiter.type === msg.type);
        if (index >= 0) {
          const [waiter] = waiters.splice(index, 1);
          handled = true;
          waiter.resolve(msg);
        }
        if (!handled) backlog.push(msg);
      };
      ws.onerror = () => failWaiters(new Error("signaling failed"));
      ws.onclose = () => {
        closed = true;
        failWaiters(new Error("signaling connection closed"));
      };
      await withTimeout(waitWebSocket(ws), "WebSocket signaling connection", signalTimeoutMs);
      if (ws.protocol === reconnectProtocol) {
        ws.send(JSON.stringify({
          type: "signal_join",
          reconnect_token: loadReconnectToken(token, role, protocol),
        }));
      }
      return {
        send(msg) {
          if (closed || ws.readyState !== WebSocket.OPEN) throw new Error("signaling connection closed");
          ws.send(JSON.stringify(msg));
        },
        wait(type, label = type) {
          const index = backlog.findIndex((msg) => msg.type === type);
          if (index >= 0) return Promise.resolve(backlog.splice(index, 1)[0]);
          const waiter = {};
          const promise = new Promise((resolve, reject) => {
            Object.assign(waiter, { type, resolve, reject });
            waiters.push(waiter);
          });
          return withTimeout(promise, label, signalTimeoutMs).finally(() => {
            const current = waiters.indexOf(waiter);
            if (current >= 0) waiters.splice(current, 1);
          });
        },
        on(type, handler) {
          const typeHandlers = handlers.get(type) || [];
          typeHandlers.push(handler);
          handlers.set(type, typeHandlers);
          for (let index = 0; index < backlog.length;) {
            if (backlog[index].type !== type) {
              index++;
              continue;
            }
            handler(backlog.splice(index, 1)[0]);
          }
        },
        reconnectSupported: () => reconnectSupported,
        close() {
          closed = true;
          failWaiters(new Error("signaling canceled"));
          if (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING) ws.close();
        },
      };
    }

    function sendLocalCandidate(signal, candidate, types) {
      if (!candidate) return;
      const type = iceCandidateType(candidate);
      if (type) types.add(type);
      try {
        signal.send({ type: "candidate", candidate: candidate.toJSON() });
      } catch {}
    }

    function makeTransport(dc, extraDataChannels = [], pendingPrimaryMessages = []) {
      const dataChannels = Array.isArray(extraDataChannels)
        ? extraDataChannels.filter(Boolean)
        : [extraDataChannels].filter(Boolean);
      const channels = [dc, ...dataChannels];
      let nextDataChannel = 0;
      for (const channel of channels) channel.binaryType = "arraybuffer";
      const queue = [];
      const waiters = [];
      const bufferWaiters = new Map(channels.map((channel) => [channel, []]));
      const pendingEnvelopes = new Map();
      let nextEnvelopeSeq = 0;
      let closed = false;
      let closeError = null;
      let lastSendWaitMs = 0;
      let totalSendWaitMs = 0;
      let sendWaitCount = 0;
      let maxBufferedBytes = 0;
      let receivedBytes = 0;
      let sentBytes = 0;
      let dataReceivedBytes = 0;
      let dataSentBytes = 0;
      for (const channel of channels) {
        try {
          channel.bufferedAmountLowThreshold = bufferLowBytes;
        } catch {}
      }
      const notifyBufferWaiters = (channel) => {
        if (channel.bufferedAmount > bufferLowBytes) return;
        for (const waiter of bufferWaiters.get(channel).splice(0)) waiter();
      };
      const fail = (err) => {
        if (closed) return;
        closed = true;
        closeError = err;
        while (waiters.length) waiters.shift().reject(err);
        for (const channelWaiters of bufferWaiters.values()) {
          while (channelWaiters.length) channelWaiters.shift()();
        }
      };
      const deliver = (data) => {
        const waiter = waiters.shift();
        if (waiter) waiter.resolve(data);
        else queue.push(data);
      };
      const receive = async (channel, event) => {
        if (closed) return;
        let data = "";
        if (typeof event.data === "string") data = event.data;
        else if (event.data instanceof ArrayBuffer) data = new Uint8Array(event.data);
        else if (event.data instanceof Blob) data = new Uint8Array(await event.data.arrayBuffer());
        const frameBytes = frameByteLength(data);
        receivedBytes += frameBytes;
        if (channel !== dc) dataReceivedBytes += frameBytes;
        const seq = wireEnvelopeSequence(data);
        if (seq === null) {
          deliver(data);
          return;
        }
        if (seq < nextEnvelopeSeq || pendingEnvelopes.has(seq)) {
          fail(new Error(`duplicate transfer frame sequence ${seq}`));
          return;
        }
        pendingEnvelopes.set(seq, data);
        while (pendingEnvelopes.has(nextEnvelopeSeq)) {
          deliver(pendingEnvelopes.get(nextEnvelopeSeq));
          pendingEnvelopes.delete(nextEnvelopeSeq++);
        }
      };
      for (const channel of channels) {
        channel.onbufferedamountlow = () => notifyBufferWaiters(channel);
        channel.onmessage = (event) => receive(channel, event);
        channel.onerror = () => fail(new Error("transfer connection failed"));
        channel.onclose = () => fail(new Error("transfer connection closed"));
      }
      for (const event of pendingPrimaryMessages.splice(0)) receive(dc, event);
      const sendOn = async (channel, obj) => {
        const payload = typeof obj === "string" || obj instanceof ArrayBuffer || ArrayBuffer.isView(obj)
          ? obj
          : JSON.stringify(obj);
        const waitStarted = performance.now();
        while (channel.bufferedAmount > bufferHighBytes) {
          if (closed || channel.readyState !== "open") throw closeError || new Error("transfer connection closed");
          await waitForBuffer(channel);
        }
        lastSendWaitMs = performance.now() - waitStarted;
        totalSendWaitMs += lastSendWaitMs;
        if (lastSendWaitMs > 0) sendWaitCount++;
        maxBufferedBytes = Math.max(maxBufferedBytes, channels.reduce((sum, current) => sum + current.bufferedAmount, 0));
        if (closed || channel.readyState !== "open") throw closeError || new Error("transfer connection closed");
        channel.send(payload);
        const frameBytes = frameByteLength(payload);
        sentBytes += frameBytes;
        if (channel !== dc) dataSentBytes += frameBytes;
      };
      return {
        async send(obj) {
          await sendOn(dc, obj);
        },
        async sendData(obj) {
          const channel = dataChannels.length
            ? dataChannels[nextDataChannel++ % dataChannels.length]
            : dc;
          await sendOn(channel, obj);
        },
        hasDataChannel: () => dataChannels.some((channel) => channel.readyState === "open"),
        dataChannelCount: () => dataChannels.length,
        async recv() {
          if (queue.length) return queue.shift();
          if (closed) throw closeError || new Error("transfer connection closed");
          return new Promise((resolve, reject) => waiters.push({ resolve, reject }));
        },
        close() {
          fail(new Error("transfer canceled"));
          for (const channel of channels) {
            if (channel.readyState === "open" || channel.readyState === "connecting") channel.close();
          }
        },
        metrics: () => ({
          bufferedBytes: channels.reduce((sum, channel) => sum + channel.bufferedAmount, 0),
          bufferLimit: bufferHighBytes * channels.length,
          lastWaitMs: lastSendWaitMs,
          totalWaitMs: totalSendWaitMs,
          waitCount: sendWaitCount,
          maxBufferedBytes,
          sentBytes,
          receivedBytes,
          dataSentBytes,
          dataReceivedBytes,
        }),
      };

      function waitForBuffer(channel) {
        return new Promise((resolve) => {
          let timer = null;
          const resolveWaiter = () => {
            clearTimeout(timer);
            const waitersForChannel = bufferWaiters.get(channel);
            const index = waitersForChannel.indexOf(resolveWaiter);
            if (index >= 0) waitersForChannel.splice(index, 1);
            resolve();
          };
          timer = setTimeout(resolveWaiter, 20);
          bufferWaiters.get(channel).push(resolveWaiter);
          notifyBufferWaiters(channel);
        });
      }
    }

    async function connectPrimaryPeer({ role, signalToken, task, protocol, iceMode, unorderedData, raceMode = "" }) {
      const connectTimeoutMs = iceMode === "direct" ? directTimeoutMs : signalTimeoutMs;
      let peer = null;
      try {
        peer = await createPeer(role, signalToken, task, protocol, iceMode, unorderedData);
        peer.dc = await withTimeout(Promise.resolve(peer.dc), "WebRTC DataChannel", connectTimeoutMs);
        const pendingPrimaryMessages = [];
        peer.dc.binaryType = "arraybuffer";
        const control = raceMode ? makeRaceControl(peer.dc, pendingPrimaryMessages, task) : null;
        if (!control) peer.dc.onmessage = (event) => pendingPrimaryMessages.push(event);
        await waitDataChannelOpen(peer.dc, connectTimeoutMs, peer.pc);
        if (unorderedData) {
          peer.dataDC = await withTimeout(Promise.resolve(peer.dataDC), "WebRTC data channel", connectTimeoutMs);
          await waitDataChannelOpen(peer.dataDC, connectTimeoutMs, peer.pc);
        }
        return { peer, pendingPrimaryMessages, control, iceMode, signalToken, connectTimeoutMs };
      } catch (err) {
        if (peer) {
          if (err && typeof err === "object") {
            err.retrySignal = peer.signal;
            err.diagnostics = peer.iceDiagnostics?.();
          }
          peer.close();
        }
        throw err;
      }
    }

    async function connectRacedPeer(config) {
      const { role, token, task, protocol, onRelayStart = () => {} } = config;
      const relayToken = await iceRaceToken(token, "relay");
      const states = new Map();
      let settled = false;
      let committing = false;
      let relayTimer = null;
      let preferenceTimer = null;

      return new Promise((resolve, reject) => {
        const cancelTimers = () => {
          clearTimeout(relayTimer);
          clearTimeout(preferenceTimer);
        };
        const cancelLosers = (winner) => {
          for (const [mode, state] of states) {
            if (mode !== winner) state.task.cancel();
          }
        };
        const finish = (candidate) => {
          if (settled) return;
          settled = true;
          cancelTimers();
          cancelLosers(candidate.iceMode);
          candidate.peer.signalTokens = [token, relayToken];
          resolve(candidate);
        };
        const failRace = () => {
          if (settled || states.size < 2 || [...states.values()].some((state) => state.status !== "failed")) return;
          settled = true;
          cancelTimers();
          const failures = [...states.entries()].map(([mode, state]) => `${mode}: ${state.error?.message || state.error}`).join("; ");
          const err = new Error(`WebRTC direct/TURN race failed (${failures})`);
          err.retrySignal = {
            reconnectSupported: () => [...states.values()].some((state) => (
              state.peer?.signal?.reconnectSupported()
              || state.error?.retrySignal?.reconnectSupported?.()
            )),
          };
          err.diagnostics = mergeRaceDiagnostics(states);
          reject(err);
        };
        const commit = async (candidate) => {
          if (settled || committing) return;
          committing = true;
          try {
            candidate.control.send("kigo_route_commit", candidate.iceMode);
            const ack = await candidate.control.wait("kigo_route_ack", signalTimeoutMs);
            if (ack.mode !== candidate.iceMode) throw new Error("WebRTC route acknowledgement mismatch");
            finish(candidate);
          } catch (err) {
            committing = false;
            fail(candidate.iceMode, err);
          }
        };
        const ready = (mode, candidate) => {
          if (settled) {
            candidate.peer.close();
            return;
          }
          const state = states.get(mode);
          Object.assign(state, { status: "ready", peer: candidate.peer, candidate });
          if (role === "receiver") {
            candidate.control.wait("kigo_route_commit", signalTimeoutMs).then((message) => {
              if (settled) return;
              if (message.mode !== mode) throw new Error("WebRTC route commit mismatch");
              candidate.control.send("kigo_route_ack", mode);
              finish(candidate);
            }).catch((err) => fail(mode, err));
            return;
          }
          if (mode === "direct") {
            commit(candidate);
            return;
          }
          const direct = states.get("direct");
          if (direct?.status === "failed") {
            commit(candidate);
            return;
          }
          preferenceTimer = setTimeout(() => {
            const preferred = states.get("direct")?.candidate;
            commit(preferred || candidate);
          }, directPreferenceMs);
        };
        const fail = (mode, err) => {
          if (settled) return;
          const state = states.get(mode);
          if (!state || state.status === "failed") return;
          Object.assign(state, { status: "failed", error: err });
          state.task.cancel();
          if (mode === "direct") {
            startRelay();
            const relay = states.get("relay")?.candidate;
            if (relay && role === "sender") commit(relay);
          }
          failRace();
        };
        const start = (mode, signalToken) => {
          if (settled || states.has(mode)) return;
          const child = createChildTask(task);
          const state = { status: "connecting", task: child, peer: null, candidate: null, error: null };
          states.set(mode, state);
          const useParallelData = Boolean(config.parallelData) && mode !== "relay";
          const unorderedData = Boolean(config.unorderedData) && !useParallelData;
          connectPrimaryPeer({
            role,
            signalToken,
            task: child,
            protocol,
            iceMode: mode,
            unorderedData,
            raceMode: mode,
          }).then((candidate) => ready(mode, candidate), (err) => fail(mode, err));
        };
        const startRelay = () => {
          if (settled || states.has("relay")) return;
          Promise.resolve(onRelayStart({ delayMs: raceDelayMs })).catch(() => {});
          start("relay", relayToken);
        };

        start("direct", token);
        relayTimer = setTimeout(startRelay, raceDelayMs);
      });
    }

    async function runPeerSession(config, handler) {
      const {
        role,
        token,
        task,
        protocol = "transfer",
        onRetry = () => {},
        onParallelFallback = () => {},
      } = config;
      for (let attempt = 1; attempt <= reconnectAttempts; attempt++) {
        let peer = null;
        let pipe = null;
        let removePipeCleanup = () => {};
        try {
          const connectStarted = performance.now();
          const connected = config.directFirst
            ? await connectRacedPeer({ ...config, role, token, task, protocol })
            : await connectPrimaryPeer({
              role,
              signalToken: token,
              task,
              protocol,
              iceMode: "all",
              unorderedData: Boolean(config.unorderedData) && !Boolean(config.parallelData),
            });
          const { iceMode, signalToken, connectTimeoutMs, pendingPrimaryMessages } = connected;
          peer = connected.peer;
          const useParallelData = Boolean(config.parallelData) && iceMode !== "relay";
          const primaryClose = peer.close;
          const dataPeers = [];
          peer.pcs = [peer.pc];
          peer.signalTokens ||= [signalToken];
          peer.close = () => {
            for (const dataPeer of dataPeers) dataPeer.close();
            primaryClose();
          };
          if (useParallelData) {
            try {
              for (let lane = 1; lane <= 2; lane++) {
                const laneToken = await parallelLaneToken(signalToken, lane);
                const dataPeer = await createPeer(role, laneToken, task, protocol, iceMode, false);
                dataPeers.push(dataPeer);
                peer.pcs.push(dataPeer.pc);
                peer.signalTokens.push(laneToken);
                dataPeer.dc = await withTimeout(Promise.resolve(dataPeer.dc), `WebRTC data path ${lane}`, connectTimeoutMs);
                await waitDataChannelOpen(dataPeer.dc, connectTimeoutMs, dataPeer.pc);
              }
            } catch (err) {
              if (task.canceled || peer.dc.readyState !== "open") throw err;
              for (const dataPeer of dataPeers) dataPeer.close();
              dataPeers.length = 0;
              peer.pcs = [peer.pc];
              await onParallelFallback({ err, iceMode });
            }
          }
          const connectionMs = performance.now() - connectStarted;
          const dataChannels = useParallelData && dataPeers.length > 0
            ? dataPeers.map((dataPeer) => dataPeer.dc)
            : [peer.dataDC].filter(Boolean);
          pipe = makeTransport(peer.dc, dataChannels, pendingPrimaryMessages);
          removePipeCleanup = task.addCleanup(() => pipe.close());
          return await handler({ attempt, connectionMs, iceMode, peer, pipe });
        } catch (err) {
          if (!shouldRetry(err, peer?.signal || err?.retrySignal, attempt, task)) throw err;
          await onRetry({
            err,
            attempt,
            nextAttempt: attempt + 1,
            nextMode: config.directFirst ? "race" : "all",
            maxAttempts: reconnectAttempts,
            diagnostics: err?.diagnostics || peer?.iceDiagnostics?.(),
          });
          if (reconnectDelayMs > 0) await delay(reconnectDelayMs);
        } finally {
          removePipeCleanup();
          if (pipe) pipe.close();
          if (peer) peer.close();
        }
      }
      throw new Error("WebRTC reconnect attempts exhausted");
    }

    function shouldRetry(err, signal, attempt, task) {
      if (task.canceled || attempt >= reconnectAttempts || !signal?.reconnectSupported()) return false;
      const message = String(err?.message || err || "").toLowerCase();
      return ["closed", "failed", "timed out", "connection"].some((part) => message.includes(part));
    }

    function signalURL(token, role, protocol) {
      const scheme = location.protocol === "https:" ? "wss:" : "ws:";
      const url = new URL(`${scheme}//${location.host}/api/signal/${token}`);
      url.searchParams.set("role", role);
      if (protocol === "note") url.searchParams.set("protocol", protocol);
      return url.toString();
    }

    function reconnectKey(token, role, protocol = "transfer") {
      const protocolPrefix = protocol === "transfer" ? "" : `${protocol}:`;
      return `${reconnectStoragePrefix}${protocolPrefix}${role}:${token}`;
    }

    function loadReconnectToken(token, role, protocol) {
      try {
        return sessionStorage.getItem(reconnectKey(token, role, protocol)) || "";
      } catch {
        return "";
      }
    }

    function storeReconnectToken(token, role, value, protocol) {
      if (!/^[A-Za-z0-9_-]{32,128}$/.test(value)) return;
      try {
        sessionStorage.setItem(reconnectKey(token, role, protocol), value);
      } catch {}
    }

    function clearReconnectToken(token, role, protocol = "transfer") {
      try {
        sessionStorage.removeItem(reconnectKey(token, role, protocol));
      } catch {}
    }

    return {
      clearReconnectToken,
      runPeerSession,
      waitWebSocket,
      withTimeout,
    };
  }

  async function rtcConfig(iceMode = "all") {
    const response = await fetch("/api/ice");
    if (!response.ok) throw new Error(`ICE configuration failed (${response.status})`);
    const config = await response.json();
    if (iceMode === "host") {
      config.iceServers = [];
    } else if (iceMode === "direct") {
      config.iceServers = filterICEServers(config.iceServers, (url) => !isTURNURL(url));
    } else if (iceMode === "relay") {
      config.iceServers = filterICEServers(config.iceServers, isTURNURL);
      config.iceTransportPolicy = "relay";
      if (!config.iceServers.length) throw new Error("TURN fallback is not configured");
    }
    return config;
  }

  function filterICEServers(servers, keepURL) {
    const filtered = [];
    for (const server of Array.isArray(servers) ? servers : []) {
      const urls = (Array.isArray(server.urls) ? server.urls : [server.urls]).filter((url) => keepURL(String(url || "")));
      if (urls.length) filtered.push({ ...server, urls });
    }
    return filtered;
  }

  function isTURNURL(url) {
    return /^turns?:/i.test(url);
  }

  function createChildTask(parent) {
    const cleanups = new Set();
    let removeParentCleanup = () => {};
    const child = {
      canceled: false,
      addCleanup(fn) {
        cleanups.add(fn);
        return () => cleanups.delete(fn);
      },
      cancel() {
        if (this.canceled) return;
        this.canceled = true;
        removeParentCleanup();
        for (const cleanup of [...cleanups]) {
          try {
            cleanup();
          } catch {}
        }
        cleanups.clear();
      },
    };
    removeParentCleanup = parent.addCleanup(() => child.cancel());
    return child;
  }

  function makeRaceControl(dc, pendingMessages, task) {
    const backlog = [];
    const waiters = [];
    task.addCleanup(() => {
      const err = new Error("WebRTC route candidate canceled");
      while (waiters.length) waiters.shift().reject(err);
      backlog.length = 0;
    });
    dc.onmessage = (event) => {
      const message = parseRaceControl(event.data);
      if (!message) {
        pendingMessages.push(event);
        return;
      }
      const index = waiters.findIndex((waiter) => waiter.type === message.type);
      if (index < 0) {
        backlog.push(message);
        return;
      }
      waiters.splice(index, 1)[0].resolve(message);
    };
    return {
      send(type, mode) {
        dc.send(JSON.stringify({ type, version: 1, mode }));
      },
      wait(type, timeoutMs) {
        const index = backlog.findIndex((message) => message.type === type);
        if (index >= 0) return Promise.resolve(backlog.splice(index, 1)[0]);
        const waiter = { type, resolve: null, reject: null };
        const promise = new Promise((resolve, reject) => Object.assign(waiter, { resolve, reject }));
        waiters.push(waiter);
        return withTimeout(promise, type, timeoutMs).finally(() => {
          const current = waiters.indexOf(waiter);
          if (current >= 0) waiters.splice(current, 1);
        });
      },
    };
  }

  function parseRaceControl(value) {
    if (typeof value !== "string") return null;
    try {
      const message = JSON.parse(value);
      if (message?.version !== 1 || !["direct", "relay"].includes(message.mode)) return null;
      if (!["kigo_route_commit", "kigo_route_ack"].includes(message.type)) return null;
      return message;
    } catch {
      return null;
    }
  }

  function mergeRaceDiagnostics(states) {
    const diagnostics = [...states.values()]
      .map((state) => state.peer?.iceDiagnostics?.() || state.error?.diagnostics)
      .filter(Boolean);
    return {
      iceState: "failed",
      gatheringState: diagnostics.map((entry) => entry.gatheringState).filter(Boolean).join("/") || "unknown",
      localTypes: [...new Set(diagnostics.flatMap((entry) => entry.localTypes || []))].sort(),
      remoteTypes: [...new Set(diagnostics.flatMap((entry) => entry.remoteTypes || []))].sort(),
    };
  }

  async function iceRaceToken(token, mode) {
    const input = encoder.encode(`kigo-ice-race-v1:${token}:${mode}`);
    const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", input));
    return [...digest].map((value) => value.toString(16).padStart(2, "0")).join("");
  }

  async function parallelLaneToken(token, lane) {
    const input = encoder.encode(`kigo-parallel-v1:${token}:${lane}`);
    const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", input));
    return [...digest].map((value) => value.toString(16).padStart(2, "0")).join("");
  }

  function makeRemoteCandidateQueue(pc, types) {
    const pending = [];
    return {
      async add(msg) {
        if (!msg.candidate) return;
        const type = iceCandidateType(msg.candidate);
        if (type) types.add(type);
        if (!pc.remoteDescription) pending.push(msg.candidate);
        else await pc.addIceCandidate(msg.candidate);
      },
      async flush() {
        while (pending.length) await pc.addIceCandidate(pending.shift());
      },
    };
  }

  function iceCandidateType(candidate) {
    const text = String(candidate?.candidate || candidate || "");
    return (text.match(/\styp\s(\w+)(?:\s|$)/i)?.[1] || "").toLowerCase();
  }

  function describeICEFailure(pc, types) {
    return {
      iceState: pc?.iceConnectionState || "unknown",
      gatheringState: pc?.iceGatheringState || "unknown",
      localTypes: [...types.local].sort(),
      remoteTypes: [...types.remote].sort(),
    };
  }

  function frameByteLength(value) {
    if (typeof value === "string") return encoder.encode(value).length;
    if (value instanceof ArrayBuffer || ArrayBuffer.isView(value)) return value.byteLength;
    return 0;
  }

  function wireEnvelopeSequence(value) {
    const bytes = value instanceof Uint8Array
      ? value
      : value instanceof ArrayBuffer
        ? new Uint8Array(value)
        : ArrayBuffer.isView(value)
          ? new Uint8Array(value.buffer, value.byteOffset, value.byteLength)
          : null;
    if (!bytes || bytes.length < 12 || bytes[0] !== 0x4b || bytes[1] !== 0x47 || bytes[2] !== 0x45 || bytes[3] !== 0x31) {
      return null;
    }
    const seq = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength).getBigUint64(4, false);
    return seq <= BigInt(Number.MAX_SAFE_INTEGER) ? Number(seq) : null;
  }

  function waitDataChannelOpen(dc, timeoutMs, pc = null) {
    if (dc.readyState === "open") return Promise.resolve();
    return withTimeout(new Promise((resolve, reject) => {
      const cleanup = () => {
        dc.removeEventListener("open", onOpen);
        dc.removeEventListener("error", onError);
        dc.removeEventListener("close", onClose);
        pc?.removeEventListener("iceconnectionstatechange", onICEStateChange);
      };
      const finish = (callback, value) => {
        cleanup();
        callback(value);
      };
      const onOpen = () => finish(resolve);
      const onError = () => finish(reject, new Error("DataChannel failed"));
      const onClose = () => finish(reject, new Error("DataChannel closed before opening"));
      const onICEStateChange = () => {
        if (["failed", "closed"].includes(pc?.iceConnectionState)) {
          finish(reject, new Error(`WebRTC ICE ${pc.iceConnectionState}`));
        }
      };
      dc.addEventListener("open", onOpen, { once: true });
      dc.addEventListener("error", onError, { once: true });
      dc.addEventListener("close", onClose, { once: true });
      pc?.addEventListener("iceconnectionstatechange", onICEStateChange);
      onICEStateChange();
    }), "WebRTC DataChannel", timeoutMs);
  }

  function waitWebSocket(ws) {
    return new Promise((resolve, reject) => {
      ws.addEventListener("open", resolve, { once: true });
      ws.addEventListener("error", () => reject(new Error("WebSocket failed")), { once: true });
    });
  }

  function delay(ms) {
    return new Promise((resolve) => setTimeout(resolve, ms));
  }

  function withTimeout(promise, label, ms) {
    let timer = null;
    return new Promise((resolve, reject) => {
      timer = setTimeout(() => reject(new Error(`${label} timed out after ${Math.ceil(ms / 1000)}s`)), ms);
      promise.then(resolve, reject);
    }).finally(() => clearTimeout(timer));
  }

  global.KigoWebRTC = Object.freeze({ create });
})(globalThis);
