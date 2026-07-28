# kigo

`kigo` is a Go prototype for native/web file, text, and shared-notepad transfer. It uses a public web/signaling service for pairing, WebRTC DataChannels for browser-compatible transfer, and a persistent encrypted service for asynchronous notepads.

## Install

macOS and Linux (`amd64` or `arm64`):

```sh
curl -fsSL https://raw.githubusercontent.com/suir1/kigo/main/scripts/install.sh | sh
```

Windows PowerShell (`amd64`):

```powershell
$installer = Join-Path $env:TEMP "kigo-install.ps1"
Invoke-WebRequest https://raw.githubusercontent.com/suir1/kigo/main/scripts/install.ps1 -OutFile $installer
& $installer
Remove-Item $installer
```

The installers select the matching release archive, prefer the latest stable release and fall back to the latest
prerelease when no stable release exists, verify it against `SHA256SUMS`, run the
downloaded binary's version check, and then replace `kigo` in the user install directory. Set
`KIGO_VERSION`, `KIGO_INSTALL_DIR`, or `KIGO_ADD_TO_PATH=1` to pin a version, choose the destination, or update
the user PATH. `KIGO_RELEASE_BASE_URL` can point to an HTTPS enterprise mirror containing the archive and
`SHA256SUMS`.

## Current status

Implemented in this version:

- GitHub Actions CI for Go checks, race detection, Windows same-port direct tests, native/browser smoke tests, release archives, and container builds
- Linux network-namespace matrix for routed direct, fixed port-preserving NAT, relay fallback, and optional routed IPv6
- SSH-driven public endpoint matrix with route/checksum assertions and redacted structured diagnostics
- Tag-driven GitHub releases with CycloneDX SBOMs, SHA-256 checksums, and keyless provenance/SBOM attestations
- Checksum-verifying macOS/Linux and Windows release installers with offline CI smoke coverage
- `kigo serve` web app + WebSocket signaling + `/api/ice`
- Capability negotiation automatically selects native TCP for two installed clients and WebRTC whenever a browser participates
- Signaling can issue room-bound, expiring native relay credentials so installed clients never receive the relay's long-term secret
- `kigo web` loopback-only browser console for native send, receive, filesystem path selection, doctor, shared notepad, live logs, share links, and cancel
- `kigo tui` interactive terminal console for native send, receive, doctor, shared notepad, live logs, share links, and cancel
- `GET /api/health` runtime health endpoint with version, transfer-room, persistent-notepad, and non-secret TURN status
- Production HTTP server limits request-header time, response-write time, idle connections, and header size
- Direct TLS enforces TLS 1.2 or newer; `serve --check-config` validates URLs and certificate/key pairs before listening
- Web responses include CSP, clickjacking, MIME-sniffing, referrer, permissions, cross-origin, and HTTPS HSTS headers
- Browser signaling WebSockets accept only the request host or configured public origin; native clients remain supported without an Origin header
- Reverse-proxy deployments can trust explicit proxy IPs/CIDRs for per-client rate limits without accepting spoofed forwarding headers from direct clients
- Optional built-in UDP TURN server for self-hosted WebRTC fallback
- Built-in TURN can constrain relay allocations to a firewall- and container-mapped UDP port range
- Optional TURN REST short-lived credentials compatible with the built-in server or an external coturn shared secret
- Built-in TURN enforces configurable global, per-credential, and per-source-IP active allocation quotas
- Built-in TURN counts socket-level UDP egress and enforces token-bucket byte quotas globally, per credential, and per source IP
- Signaling rooms have a TTL and expired rooms notify connected clients before closing
- Native and browser file/text clients stop waiting for an unpaired peer after five minutes by default; persistent notepads are available with one client
- Signaling rooms lock after a sender and receiver are paired, preventing late joins from reusing pending signals
- Signaling WebSockets enforce message size limits, read deadlines, and ping/pong heartbeats
- Signaling clients can replay the full pending ICE/offer backlog without blocking room joins
- Signaling room paths accept only 64-character hex SHA-256 room tokens
- Embedded web assets, with `--web-dir` for local development overrides
- `kigo send <path> --symlinks follow|preserve [--no-gitignore]` native file or directory send over WebRTC/relay
- `kigo recv <code> --out <dir> --on-conflict overwrite|skip|rename` native receive
- `kigo text send [text]` and `kigo text recv <code>`
- `kigo note host`, `kigo note join <code>`, and browser/local-web/TUI views for an asynchronous encrypted notepad that survives disconnected clients and service restarts
- TTY senders print a terminal QR code for the browser share link or native pairing code; `--no-qrcode` disables it
- Senders generate a six-character code by default or accept a shared custom alphanumeric/mnemonic code through CLI, browser, local web, and TUI
- Native CLI, browser, local web, and TUI receive paths normalize and validate pairing codes before joining
- Client runtime configuration resolves explicit flags, environment variables, saved non-secret settings, then local defaults in that order
- `--interface <name>` binds native signaling, relay/direct sockets, STUN probes, WebRTC ICE candidates, and LAN discovery to one interface
- External client routes detect active VPN/TUN adapters and compare the default path with a physical-interface path before selecting an outbound interface
- `kigo relay` native TCP relay for installed-client transfers
- UDP multicast discovery for LAN relays, with LAN-first rendezvous racing against an explicitly configured relay
- Native senders host an ephemeral LAN relay while pairing; receivers discover it without requiring a separately installed relay service
- New native peers can upgrade an established public relay control path to verified multi-connection LAN direct before application encryption starts
- `kigo route` shows scored route candidates without failing the command when dependencies are down
- `kigo doctor` checks signaling ICE/TURN config, two-destination STUN mapping behavior, optional relay reachability, direct-listen advertise preview, scored route candidates, and native/web route matrix
- Local route history records aggregate direct, relay, and WebRTC outcomes plus encrypted payload throughput for `route`/`doctor` scoring
- New native peers exchange direct candidates through signaling without first joining the TCP relay; three recent consecutive direct failures on the current network scope defer direct for 30 minutes on both peers before probing again
- Native CLI maps common cancel, timeout, relay, signaling, and peer-disconnect failures to clearer retry guidance
- Native TCP direct candidate race via `/api/direct/{room_token}`, with backward-compatible typed candidate metadata, global IPv6 preference, room-bound and connection-index-bound peer verification, plus relay or WebRTC fallback
- Optional `--udp-probe` keeps each peer's STUN socket open, exchanges bounded UDP candidates, runs an authenticated synchronized poke window, and applies the same NAT-aware direct timeout on both sides
- Each native peer with a configured relay automatically probes it from the direct listener's TCP port; the relay-observed mapping is exchanged as a low-priority `public` candidate and can be disabled with `--no-tcp-probe`
- Legacy relay-based direct rendezvous remains available when transport negotiation is unavailable
- New native peers negotiate `bidirectional-direct-v1`, share a bounded punch time, try receiver-initiated same-port TCP first, and use sender-initiated TCP as the reverse-direction fallback
- Successful native direct routes negotiate the same multi-connection bundle and chunk striping used by relay fallback
- Direct auxiliary setup is all-or-nothing; relay-rendezvous compatibility mode keeps the relay open until every negotiated direct connection is verified
- Native relay routes negotiate up to four parallel TCP connections by default; connection 0 carries control traffic and file chunks stripe across the encrypted data connections
- Each native relay connection has an independent HKDF-derived AES-GCM key and envelope sequence space
- Peers with different `--connections` values use the smaller count; older peers and WebRTC/browser routes remain single-connection
- Chunk striping is capability-negotiated separately from connection count, preserving compatibility with older multi-connection native peers
- Native striped receivers accept authenticated chunks out of order, reject overlapping ranges, and defer completion until every declared byte is covered
- Native striped receivers defer early data-channel messages until resume control negotiation completes, because independent TCP connections do not preserve cross-connection ordering
- Interrupted striped receives truncate `.kigopart` to the verified contiguous prefix before reconnect resume
- File manifests include an optional imohash-style `sample_sha256`; peers negotiate deferred full hashing so completed-file checks read at most 1 MiB on each side, while files that actually transfer still receive full SHA-256 verification
- Native striped senders run one bounded worker per data connection, preserving per-connection envelope order while physical writes proceed concurrently
- Chunk scheduling favors the path with fewer pending plaintext bytes, so a backlogged connection receives less new work
- Native send completion flushes every path worker before emitting `stream_end`, and reports per-path bytes, chunk counts, and observed send rate
- Native relay transfers automatically reconnect up to three total attempts and resume file data from `.kigopart`
- Resume negotiation verifies the receiver's partial-file prefix SHA-256 and explicitly agrees on the accepted offset
- Native receive conflict policies can replace, skip, or rename differing existing files; skip decisions are synchronized through resume negotiation
- Native directory sends preserve empty directories, directory permission bits, and modification times
- Native sends follow file symlinks by default; `--symlinks preserve` transfers safe relative links as links
- Native directory sends respect `.gitignore` rules from the source directory and its ancestors by default
- Native send preflight reports file, directory, symlink, byte, and ignored-path counts before creating the pairing room
- Web UI for send file, send text, and receive
- Web UI can cancel an in-flight transfer and restores controls after cancellation
- Web UI maps common room, signaling, timeout, and connection-close failures to clearer retry guidance
- Web pairing links with `#c=<code>` auto-start receiving
- Web notepad links with `#n=<code>` auto-open `main`; `#n=<code>&p=<pad>` selects a custom pad
- Native CLI, browser, local web, and TUI notepads edit immediately, broadcast to connected clients, and restore the latest encrypted service snapshot when opened later
- Notepad drafts are encrypted locally with a code-derived AES-128-GCM key, expire after seven days, and restore only for the same code, role, and pad
- Web UI supports selecting multiple files; Chromium-style folder picking is available when the browser supports `webkitdirectory`
- Web receivers get a `Download all as ZIP` link for multi-file or directory transfers; ZIPs preserve empty directories, Unix modes, timestamps, and symlink entries
- Native and browser peers reject unsupported hello handshake versions and missing nonces
- Native and browser hello handshakes negotiate optional gzip chunk compression; older peers transparently use identity chunks
- HKDF-SHA256 + AES-128-GCM encrypted manifest/chunks
- Native and browser receivers reject encrypted envelopes with unsupported versions or out-of-order sequence numbers
- Native and browser receivers validate manifest version, item kind, size, chunk size, SHA-256 shape, duplicate paths, path traversal, non-directory parents, and unsafe symlink targets before receiving chunks
- SHA-256 item integrity verification before native receivers rename `.kigopart` or return text
- Native receivers restore manifest permission bits and modification times after finalizing files
- Native receivers reject chunks with negative, skipped, or out-of-bounds offsets before writing data
- Browser receivers verify SHA-256 before showing text or download links
- Chromium receivers persist file prefixes in OPFS every 4 MiB and resume matching name/size/SHA-256 manifests after interruption or page reload
- Chromium receivers use a same-origin OPFS Dedicated Worker with synchronous access handles when available, so file writes do not occupy the transfer/UI thread; other browsers retain the asynchronous OPFS or memory fallback
- WebRTC signaling assigns random per-role reconnect tokens; a refreshed browser can reclaim its sender/receiver slot with the same pairing code without restarting the native peer
- Reconnect tokens are exchanged only after the `kigo-reconnect-v1` WebSocket subprotocol is negotiated, are never placed in URLs, and remain scoped to the current browser tab or native command
- Each WebRTC retry creates a fresh peer connection, hello nonce pair, HKDF output, AES-GCM session, and envelope sequence while resume reuses only the verified plaintext prefix
- Completed OPFS files remain available as a seven-day content cache so large downloads do not require a second full in-memory copy
- Browser receivers reject chunks with negative, skipped, or out-of-bounds offsets before buffering data
- Browser senders hash selected files in slices instead of loading whole files into memory
- Browser receivers verify files and build multi-file ZIP downloads from received chunks without an extra full-file copy
- Browser and native WebRTC sends apply DataChannel backpressure before queueing more chunks
- Web-Web peers negotiate authenticated binary chunk frames, removing Base64 and JSON overhead from file/text payloads; native-Web peers fall back to the legacy envelope automatically
- Kigo includes a compatibility-negotiated reliable-unordered file-channel experiment with encrypted envelope sequence reordering, but keeps it disabled by default because clean-LAN measurements were slower than the ordered single-channel path
- Web-Web peers with a sufficient SCTP `maxMessageSize` negotiate 192 KiB chunks; legacy and native paths remain at 64 KiB
- Web-Web ICE runs host-only LAN, STUN direct, and relay-only TURN attempts in sequence; each stage rebuilds both peer connections so fast public candidates cannot preempt a viable LAN route and fallback cannot remain stuck behind failed checks
- Host-only Web-Web paths use 192 KiB chunks; STUN/TURN paths use 64 KiB chunks to reduce SCTP fragmentation and head-of-line delay on slower or lossy routes
- Services advertising TURN also advertise the same endpoint as the first STUN server, avoiding dependence on public Google STUN for direct candidates; direct fallback logs candidate types without exposing addresses
- Native and browser senders compress chunks independently only when gzip saves at least 1% and 32 bytes; three consecutive misses disable further attempts for that item
- Chunk compression preserves plaintext offsets, resume hashes, mux scheduling, progress accounting, and final SHA-256 verification
- Native and browser receivers reject unnegotiated encodings, corrupt gzip data, oversized encoded chunks, and decompressed chunks above the negotiated protocol limit
- Pion transports expose send backlog plus the previous backpressure wait; native multi-file schedulers adapt chunk budgets between 64, 32, and 16 KiB as pressure changes, while browser sends use 64 KiB legacy or 192 KiB negotiated binary chunks
- Native and browser multi-file sends use weighted deficit round-robin scheduling; smaller remaining streams receive larger quanta while transport sends remain backpressure-gated
- Manifests can carry explicit `streams[{id,item}]` bindings; new native and browser sends allocate stream IDs independently from item indexes
- Legacy manifests without stream bindings remain compatible through an implicit `stream == item` plan
- Transfer and resume messages resolve `stream_open`, `chunk`, `stream_end`, and resume offsets through the manifest mux plan
- Native and browser progress accounting is keyed by logical stream while still displaying file/text names
- Native mux planning, stream/item lookup, frame validation, resume validation, and stream lifecycle tracking are centralized in `internal/mux`
- Native handshake, encrypted envelopes, transfer control messages, and typed receive events are centralized behind `TransferSession`
- Native file/text persistence, `.kigopart` resume state, integrity checks, and final rename are centralized behind `ReceiveStore`
- Browser and native CLI transfers show progress with transferred bytes, percentage, and transfer rate
- Native WebRTC close waits briefly for queued DataChannel bytes so final acknowledgements are not dropped
- Browser transfer logs keep local performance telemetry: selected ICE pair, RTC byte rate, RTT, DataChannel backpressure, AES/compression time, and OPFS queue/write time; telemetry is never sent to the service
- Browser DataChannel backpressure uses a bounded 4 MiB high-water mark with `bufferedamountlow` wakeups, keeping the transfer queue bounded without polling-only waits
- Route history is partitioned into hashed network scopes so failures and throughput learned on one Wi-Fi, hotspot, VPN, or routed interface do not affect another
- `route` and `doctor` attach live signaling/relay latency probes to candidates and apply bounded score adjustments
- `route` and `doctor` reuse one UDP socket across two STUN destinations to classify open, stable, or destination-dependent mappings; native direct and WebRTC scores use the result as a heuristic
- Multi-connection direct/relay senders persist per-connection EWMA throughput and restore normalized `0.5-2.0` path weights on the next transfer
- Physical chunk scheduling blends historical weights with current send rate and queue pressure, so stale history decays while slow paths stop accumulating work
- `.kigopart` receive path before final rename

## Run locally

```sh
go run ./cmd/kigo serve --listen 127.0.0.1:9100
```

Open `http://127.0.0.1:9100` for the web app.

For a repeatable local Web-Web throughput sample, run the browser smoke matrix with a generated file:

```sh
KIGO_SMOKE_WEB_WEB_BYTES=$((64 * 1024 * 1024)) ./scripts/smoke_native_web.sh
```

The Web-Web section prints sender and receiver telemetry. Treat `Path: direct P2P` and
`srflx/srflx` or `host/host` as direct-path evidence; a same-machine `srflx/srflx`
run is a hairpin benchmark, not a physical two-device LAN ceiling.

Installed-client terminal console:

```sh
kigo tui
kigo --relay 127.0.0.1:9000 --relay-pass secret tui
```

Use Left/Right on the mode row to choose Send, Receive, Doctor, or Notepad. Tab and Up/Down move between fields,
Enter on Path or Output opens the path browser, Enter elsewhere changes an option or starts the selected task,
and `c`/Esc cancels a running task. In the path browser, type to filter, use Up/Down to select, Enter/Right to
open or choose, Left to move to the parent directory, Tab to switch name/modified sorting, and Esc to return
without changing the value. The TUI and CLI call the same in-process client task module, so direct TCP, relay
fallback, WebRTC, encryption, resume, mux, and route history remain shared without rebuilding command-line
arguments or parsing child-process output.

The Notepad mode can create or open a code and select one pad. As soon as the persistent service is connected it provides a multiline editor,
publishes changes after 250 ms, and accepts `Ctrl+S` to sync immediately, `Ctrl+L` to clear, and Esc to leave.
It uses the same in-process note controller as the loopback web console rather than launching an interactive
child process.

The TUI remembers its last mode, send path, receive directory, symlink mode, gitignored-file option, conflict
policy, and Doctor timeout. CLI transfers update those preferences only when explicitly requested:

```sh
kigo send ./archive --remember
kigo recv K7M9Q2 --out ./downloads --on-conflict rename --remember
```

Configure a public deployment once instead of repeating client flags:

```sh
kigo config set service https://kigo.example
kigo config set relay relay.example.com:9000
kigo config set tls-ca /path/to/private-ca.pem
kigo config show
```

`service` sets both the signaling endpoint and browser share-link base. Use `signal` and `web-url` separately
when those endpoints differ. `tls-ca` persists the path to an additional trusted PEM CA bundle. Other saved keys are `transport`, `interface`, `avoid-vpn`, and
`no-auto-interface`; boolean settings accept `true` or `false`. Remove one with
`kigo config unset <key>` and inspect the file location with `kigo config path`.

Client resolution order is explicit CLI flag, environment variable, saved setting, then built-in default.
Supported client variables are `KIGO_SIGNAL` (with `KIGO_SIGNAL_URL` as an alias), `KIGO_WEB_URL`,
`KIGO_RELAY`, `KIGO_TRANSPORT`, `KIGO_INTERFACE`, `KIGO_AVOID_VPN`, `KIGO_NO_AUTO_INTERFACE`,
`KIGO_PROXY`, `KIGO_DISCOVERY_ADDR`, and `KIGO_RELAY_PASS`. Credential-bearing proxy URLs and relay
passwords are never persisted.

Preferences and saved client endpoints are stored in the platform user-config directory as `kigo/config.json`;
set `KIGO_CONFIG_PATH` to override the file for portable or isolated runs. Encrypted notepad drafts use the adjacent `kigo/note-drafts/` directory; set
`KIGO_NOTE_DRAFT_PATH` to override it or pass `--no-note-drafts` to disable persistence.

Installed-client local console:

```sh
kigo web
kigo web --no-open --listen 127.0.0.1:0
```

`kigo web` prints a random token in the URL fragment, accepts API requests only with that token, and refuses
non-loopback listeners. Tasks use the same typed in-process send, receive, and Doctor operations as the CLI
and TUI, so native direct/relay behavior is preserved while the generated public link still lets a browser
peer receive over WebRTC.

Native text smoke:

```sh
kigo text send "hello"
kigo text recv <code>
```

Custom sender codes are available on file, text, and notepad hosts:

```sh
kigo send ./file --code project-alpha-2026
kigo text send "hello" --code project-alpha-2026
kigo note host --code project-alpha-2026
```

Codes normalize to uppercase. They may contain 6-64 ASCII letters/digits, with single hyphens separating
mnemonic segments. Six-character codes may include spaces or hyphens as copy/paste formatting. Human-chosen
codes are generally easier to guess than generated codes; use the random default when the code itself needs to
be unpredictable.

When stdout is an interactive terminal, `send`, `text send`, and `note host`
print a QR code. Auto/WebRTC sessions encode the complete public `#c=` or
`#n=` link so a phone can open the browser peer directly. Custom notepad links
include the selected pad in the URL fragment. Forced native sessions encode only the pairing code. Redirected
output remains plain text; pass `--no-qrcode` to disable QR rendering
explicitly.

Native shared notepad:

```sh
kigo note host
kigo note join <code>
```

The notepad is a persistent shared document rather than a one-shot text transfer or two-peer rendezvous.
Enter lines to publish the selected pad; `/show`, `/clear`, and `/quit` control
a native session. The default pad is `main`; `--pad <name>` selects another.
Native CLI, browser, loopback web, and TUI clients all support one selected pad per connection. A browser can open
the printed `#n=<code>` or `#n=<code>&p=<pad>` link, or use the Notepad tab to create/open, edit, clear, and
leave. No second client is required. The public service broadcasts concurrent updates and retains an AES-GCM
encrypted snapshot for 30 days after the latest update by default. Encrypted local drafts persist for seven days.
See `docs/note.md` for storage, conflict, security, and size-limit details.

Transport selection defaults to `--transport auto`:

```sh
kigo --transport auto send ./file
kigo --transport native --relay relay.example:9000 send ./file
kigo --transport native --relay relay.example:9000 --proxy http://127.0.0.1:3128 send ./file
kigo --transport native --relay relay.example:9000 --proxy socks5://user:pass@127.0.0.1:1080 send ./file
kigo --transport webrtc send ./file
kigo --transport native --udp-probe send ./file
kigo --interface utun3 send ./file
kigo --avoid-vpn send ./file
kigo --pair-timeout 90s send ./file
```

Send and receive sessions wait up to five minutes for the peer by default. `--pair-timeout` accepts a
Go duration such as `90s` or `10m`. The deadline covers route negotiation and waiting for the initial transport;
after the peer connects, it is removed and does not limit transfer duration. Notepads use their persistent
WebSocket service instead of this pairing window.

In auto mode, each peer advertises whether it is native or web plus any configured native relay/LAN capability.
Two direct-capable native peers first exchange TCP candidates through signaling. A common client relay or the
relay advertised by `kigo serve --native-relay` is retained as fallback; without a relay, failed direct TCP
falls back to WebRTC. Common LAN-only peers continue using LAN discovery. If either peer is a browser, both use
WebRTC. `--relay` is therefore a fallback candidate in auto mode, not an unconditional transport choice.
`--transport native` permits signaling-based direct TCP and refuses WebRTC fallback; `--transport webrtc`
ignores native direct, relay, and LAN settings.

`--proxy` applies to external native TCP relay connections, including striped auxiliary connections. Supported
URLs are `http://[user:pass@]host[:port]` for HTTP CONNECT and
`socks5://[user:pass@]host[:port]`; default ports are 8080 and 1080. Selecting a proxy disables native direct
TCP and the relay-observed public TCP probe because those paths would bypass the selected proxy. LAN-discovered
relay candidates remain direct LAN connections and bypass the proxy. The setting does not proxy signaling,
WebRTC ICE, or TURN traffic; a negotiated or forced WebRTC route ignores it.

`--interface <name>` is independent from `--proxy` and constrains all native client traffic to addresses owned
by one interface. This includes signaling HTTP/WebSocket connections, the connection to an HTTP/SOCKS proxy,
native relay and direct TCP, STUN probes, Pion WebRTC ICE gathering, and IPv4 LAN relay discovery. It is intended
for VPNs, multiple uplinks, and reproducible route testing. Kigo binds portable source IPs rather than using
OS-specific device socket options; it does not create routes, so destinations unavailable through the selected
source interface fail explicitly. A wildcard `--direct-listen :0` is narrowed to a selected interface address,
and a conflicting explicit listen address is rejected. Local web and TUI child commands inherit the policy.

Without an explicit interface or proxy, Kigo inventories active adapters. If a VPN/TUN is present, it probes
the configured relay over TCP, or signaling `/api/health` when no relay is configured, through the system
default path and the preferred physical adapter concurrently. Each probe is bounded to 900 ms. The physical
path is selected only when it succeeds while the default path fails, or when it is more than 5 ms faster.
`--avoid-vpn` selects the physical adapter without probing; `--no-auto-interface` keeps normal OS routing.
Loopback targets and proxy configurations retain the default path. `route` and `doctor` report the selected
path, reason, physical adapter, target, and probe results.

Native direct rendezvous uses:

```text
WS /api/direct/{room_token}?role=sender|receiver
```

The endpoint exchanges only bounded connection metadata: direct TCP candidates, optional candidate kind/priority
metadata, negotiated connection count, route preference, whether each peer has a relay fallback, optional
UDP NAT probe capability/class and candidates, optional bidirectional support, and a shared punch time when both
peers support it. New peers retain the legacy `candidates` string array and add optional `candidate_meta`,
`bidirectional`, `peer_bidirectional`, `udp_punch`, `udp_candidates`, and `punch_at_ms`; old clients and servers
can ignore the extensions.
It does not proxy TCP connections,
encrypted frames, manifests, filenames, text, or file payloads. After rendezvous,
peers authenticate each direct
connection with the existing room-bound `kigo-direct-v1` handshake. If direct setup fails, both peers use a
relay only when both advertised one; otherwise auto mode falls back to WebRTC. Forced `--transport native`
returns an error instead of using WebRTC. An older signaling service does not advertise
`direct-rendezvous-v1`; clients with an explicit relay then retain the legacy relay-rendezvous candidate
exchange.

`--udp-probe` is opt-in for actual transfers. When both native peers enable it, each side probes the signaling
service's ICE STUN destinations, exchanges only `open`, `cone`, `symmetric`, or `unknown`, then independently
computes the same direct timeout:

- either peer `open`: at least 3.5 seconds
- both peers `cone`: at least 1.5 seconds
- one peer `symmetric`: at most 700 milliseconds
- both peers `symmetric`: at most 500 milliseconds
- otherwise: the configured `--direct-timeout`

When both peers also advertise `udp-punch-v1`, kigo retains the socket used by STUN and exchanges up to eight
UDP LAN/public candidates. At the shared `punch_at_ms`, each side sends at most 32 small datagrams during a
400 ms window while the existing bidirectional TCP race proceeds. Datagrams contain only a protocol marker,
role, punch timestamp, and a room-token-keyed truncated HMAC; they never carry transfer payloads or the pairing
code. A valid packet is acknowledged once to the observed source address.

If either peer does not advertise probe support, both retain the configured timeout and skip UDP assistance.
Probe or poke failure is advisory and does not block direct TCP, relay, or WebRTC fallback. UDP candidates are
kept separate from TCP candidates, and UDP reachability does not guarantee equivalent TCP NAT behavior.

When a native relay is configured, each direct-capable peer performs a bounded TCP `punch_probe` by connecting
to that relay from the same local port as its direct listener. A compatible relay returns the source `IP:port`
it observed, and kigo advertises that address as a low-priority `public` candidate behind global IPv6 and LAN
candidates. Peers that both advertise `bidirectional-direct-v1` receive the same near-future punch timestamp.
The receiver tries outbound same-port TCP first; if that phase fails, the sender tries the reverse direction.
The receiver opens verified mux auxiliary connections after the primary path is selected. If either peer or
the server lacks the new capability, kigo preserves the legacy receiver-dials-sender flow. This follows kiko's
relay-observed TCP mapping model; it does not configure the router through
UPnP, NAT-PMP, or PCP and cannot guarantee reachability through symmetric or endpoint-dependent NAT. Probe
failure and older relays fall back to the existing direct candidates without failing the transfer.

For a service-advertised relay, configure the same server-side secret on `serve` and `relay`. The signaling
service signs a credential bound to the SHA-256 room token and expiry time; clients receive that temporary
credential through the negotiation WebSocket and send it in the existing relay join field. The long-term secret
is never returned by an API.

Doctor:

```sh
kigo route
kigo route --json
kigo route --pair web-web
kigo route --pair web-web --ai-explain
kigo doctor
kigo doctor --json
kigo doctor --ai-explain
kigo --interface utun3 doctor --json
kigo --relay 127.0.0.1:9000 --relay-pass secret doctor
```

`route` focuses on route selection and keeps returning JSON even when dependencies are down. Use `--pair native-native`, `--pair native-web`, or `--pair web-web` to inspect one path class. `doctor` treats failed checks as a command failure.
`doctor --json` prints the selected network policy plus the same signaling, STUN NAT, relay, direct-listen, same-port socket support, and
route-matrix checks as structured output for CI or deployment scripts. STUN failure is reported as unavailable
but does not fail the whole command, because UDP can be blocked while relay or TCP paths remain usable.
Both commands include a deterministic `assessment` with a concise diagnosis, a stable recommendation token,
actionable remediation steps when needed, and a `route_result_hint` describing whether direct TCP will be
attempted and whether native relay rendezvous or payload forwarding is required. `route --pair` computes this
assessment for the selected peer combination instead of reusing the native/native conclusion.

`doctor --ai-explain` and `route --ai-explain` optionally rewrite only the human-readable diagnosis through an
OpenAI-compatible BYOK endpoint. For `route --pair`, the sanitized facts describe only the selected peer pair.
The deterministic recommendation, actions, candidate scores, and route result remain authoritative and cannot
be changed by the model. Configure it with `KIGO_AI_API_KEY` (or `OPENAI_API_KEY`), optional
`KIGO_AI_BASE_URL` (default `https://api.openai.com/v1`), and optional `KIGO_AI_MODEL`. Only a sanitized summary
containing the peer pair, route kind/score, generic fallback, NAT class, VPN/TURN booleans, fixed warnings, and
rule actions is sent. Pairing codes, IP addresses, interface names, signaling/relay URLs, file names, and file
contents are excluded. Non-loopback AI endpoints must use HTTPS and redirects are rejected.

Repeatable network validation is split into a deterministic Linux namespace
lab and a real public endpoint runner:

```sh
./scripts/smoke_note.py
./scripts/nat_lab.sh --dry-run
sudo env KIGO_BIN=/tmp/kigo ./scripts/nat_lab.sh

KIGO_SENDER_HOST=user@sender.example \
KIGO_RECEIVER_HOST=user@receiver.example \
KIGO_SIGNAL_URL=https://kigo.example \
./scripts/public_matrix.sh --dry-run
```

See `docs/network-matrix.md` for topology, required dependencies, artifact
format, public SSH execution, and the limits of synthetic NAT results.

The default ICE response advertises `stun.l.google.com` and `stun1.l.google.com`, allowing the probe to compare
two destinations without changing its local UDP port. A stable mapping is reported as `cone`, a mapping that
changes by destination as `symmetric`, and a local unchanged mapping as `open`. These names describe observed
UDP mapping behavior only; they do not prove that native TCP direct will work. Symmetric results lower native
direct confidence and make missing TURN more prominent, while relay and WebRTC fallback remain available.

Successful and failed transfers update local route history at the platform user-cache location, normally
`$HOME/Library/Caches/kigo/route-history.json` on macOS. Version 2 partitions observations into up to 16
network profiles. The profile key is a truncated SHA-256 scope derived from the routed local interface/address,
or directly from the interface selected by `--interface`;
raw IP addresses, SSIDs, gateway identifiers, and service endpoints are not written. IPv6 privacy addresses
within the same `/64` share a profile. Version 1 device-wide files remain readable and migrate into the current
scope on the next observation.

Each profile stores only route kind, aggregate success/failure counters, encrypted payload byte counts,
duration, and EWMA throughput. It does not store pairing codes, room tokens, endpoints, file names, or
manifests. Use `--route-history <path>` to select a different file or `--no-route-history` to disable recording
and historical scoring. `route` and `doctor` show the active scope plus live signaling/relay probe latency;
probe adjustments are bounded so one fast health check cannot override route availability or sustained
history. For native/native transfers, peers that both support `route-choice-v1` defer direct TCP for 30 minutes
after three consecutive direct failures in their active scopes. Both sides must agree through rendezvous before
switching immediately to relay. Missing capability fields preserve the previous direct-first behavior for older
peers.

For striped native transfers, successful senders also record aggregate bytes, chunk count, send time, and EWMA
throughput for each numeric data-connection slot. The next transfer normalizes active slots to weights between
`0.5` and `2.0`; changing `--connections` recalculates weights using only slots that still exist. During the
transfer, measured send rate is blended in progressively and queued bytes add an explicit congestion penalty.
Only numeric slot statistics are persisted; remote addresses and direct candidates are not recorded.

Service health:

```sh
curl http://127.0.0.1:9100/api/health
```

The health endpoint reports version metadata, uptime, active transfer rooms, persistent-notepad document/client
counts and TTL, public URL, server capability names including `direct-rendezvous-v1`, the advertised native relay endpoint, TURN credential mode,
active allocation counts, configured quota ceilings, cumulative built-in TURN egress, dropped bytes, and quota
rejections. It does not include TURN credentials or shared secrets.

Native file or directory smoke:

```sh
kigo send ./file-or-folder
kigo recv <code> --out ./downloads
```

Directory sends preserve the top-level directory name under the receiver output directory.
File symlinks are followed by default. Use `--symlinks preserve` to retain safe relative symlinks; absolute
targets and targets containing `..` are rejected. Empty directories and directory metadata are preserved.
Directory sends load `.gitignore` files from the source directory through its ancestors. Use
`--no-gitignore` when ignored build outputs or other normally excluded paths must be included.
Native receive defaults to `--on-conflict overwrite`. Use `skip` to leave differing existing paths untouched,
or `rename` to save as `name (1).ext`, `name (2).ext`, and so on. Matching completed files are reused under
all policies, and renamed `.kigopart` files remain resumable across reconnects.

Gzip compression is negotiated automatically when both peers support it. Each chunk is compressed
independently, so reconnect and resume offsets continue to refer to the original file. Already-compressed or
random data stays uncompressed after the sender detects that gzip is not useful.

Native/web browser smoke:

```sh
npm ci
./scripts/smoke_native_web.sh
KIGO_BROWSER_MATRIX=chromium,firefox,webkit ./scripts/smoke_browser_matrix.sh
KIGO_BROWSER_MATRIX=webkit KIGO_BROWSER_MATRIX_NATIVE_INTERFACE=en0 \
  ./scripts/smoke_browser_matrix.sh

# Run selected native/browser scenarios against an already deployed service.
KIGO_SMOKE_EXTERNAL_SERVICE=1 \
KIGO_BASE_URL=https://kigo.example \
KIGO_SMOKE_FILTER='native->web file,web->native file' \
./scripts/smoke_native_web.sh

# Trust a private or self-signed CA for native clients while retaining hostname verification.
KIGO_TLS_CA=/path/to/ca.pem \
KIGO_SMOKE_EXTERNAL_SERVICE=1 \
KIGO_BASE_URL=https://192.0.2.10 \
KIGO_SMOKE_IGNORE_TLS_ERRORS=1 \
./scripts/smoke_native_web.sh

KIGO_PUBLIC_BROWSER_URL=https://kigo.example \
KIGO_PUBLIC_BROWSER_ENGINE=firefox \
KIGO_ARTIFACT_DIR="$PWD/artifacts/public-browser-firefox" \
./scripts/smoke_public_browser.sh
```

Native client commands accept the same CA through `--tls-ca /path/to/ca.pem`
or `KIGO_TLS_CA`. The certificate is added to the system trust roots; TLS
version and hostname verification remain enabled. `KIGO_SMOKE_IGNORE_TLS_ERRORS`
only affects the test runner's browser and curl processes and must not be used
as a production trust mechanism.

Local native web console smoke:

```sh
./scripts/smoke_local_web.sh
```

Native TUI smoke:

```sh
./scripts/smoke_tui.py
```

Terminal QR smoke:

```sh
./scripts/smoke_qrcode.py
```

Direct HTTPS and production-header smoke:

```sh
./scripts/smoke_https.sh
```

Chromium stores resumable receive data in origin-private file storage. If a browser transfer is interrupted,
refresh the same tab and kigo reclaims the original signaling role with the same pairing code, then resumes any
file whose safe relative name, declared size, and full SHA-256 match the stored partial. The browser keeps the
random signaling reconnect token in `sessionStorage`; it disappears when the tab closes and is never included
in the pairing URL. Completed files remain as a seven-day cache so downloads can stream directly from browser
storage. Browsers without OPFS can reconnect in the same tab but request offset zero.

Browser notepad drafts are separate from OPFS transfer data. They are encrypted with a code-derived AES-128-GCM
key before entering origin-local storage, retain at most three recent entries as quota permits, and expire after
seven days. The pairing code, role, and pad must match to decrypt a draft. These local drafts complement the public
service's encrypted snapshot: clearing browser storage does not remove the shared document, which remains
recoverable until the service's default 30-day sliding TTL expires.

The smoke script builds a temporary `kigo` binary, starts a local service plus optional built-in TURN on an available localhost port, and drives a Playwright browser. It covers browser-side streaming SHA-256 plus transfer/notepad protocol guards, gzip transfers in both native/web directions, native-to-web file and text receive, same-code browser refresh plus OPFS resume without restarting the native sender, wrong reconnect-token rejection, web-to-native file, text, and conflict-skip sends, web-to-web file and text transfer, direct-ICE failure with synchronized TURN fallback, asynchronous native/browser and web/web notepad editing and recovery, encrypted browser-draft page recovery, notepad deep links and protocol mismatch rejection, web cancellation/error handling, web-to-native resume from an existing `.kigopart`, native directory-to-web ZIP download with `.gitignore`, empty-directory, and symlink metadata checks, and Chromium folder upload to native.

Set `PLAYWRIGHT_BROWSER=chromium|firefox|webkit` and optionally `KIGO_SMOKE_FILTER` with one or more comma-separated labels. Chromium uses the installed Chrome channel by default; set `PLAYWRIGHT_CHANNEL=` (or legacy `PLAYWRIGHT_CHROMIUM_CHANNEL=`) to use its bundled runtime. `KIGO_SMOKE_TURN_ENABLED=0` disables the local TURN server, while `KIGO_TURN_LISTEN` and `KIGO_TURN_PUBLIC_IP` override its test endpoint. `KIGO_SMOKE_NATIVE_INTERFACE` binds native peers to one interface. `KIGO_SMOKE_BROWSER_ARGS` accepts comma-separated Chromium launch arguments for ICE diagnostics.

`smoke_browser_matrix.sh` runs a compact cross-engine subset. When `KIGO_BROWSER_MATRIX_NATIVE_INTERFACE` is set, it uses two local profiles: native/web binds that interface with local TURN disabled, while web/web retains TURN. This avoids false failures caused by a same-machine TURN address and a VPN-selected native source address. The script continues after a failed browser/profile and exits nonzero after printing the full matrix result.

Playwright WebKit is a useful WebKit compatibility signal, but it is not a substitute for testing released Safari on macOS and iOS. Likewise, headless Firefox may expose only a VPN/TUN interface; if that interface cannot hairpin and the configured TURN endpoint is loopback-only, ICE will fail even though browser protocol guards pass. Use an externally reachable TURN service or a host without the TUN for the release Firefox matrix.

`smoke_public_browser.sh` connects directly to an already deployed HTTPS Kigo service. It forces
`iceTransportPolicy=relay`, transfers encrypted text and a 256 KiB random file between two browser contexts,
verifies the downloaded SHA-256, and requires the selected local candidate type to be `relay`. Its
`matrix.json` records only browser version, authenticated TURN availability, duration, byte/checksum results,
candidate types/protocols, and address-free failure diagnostics. Use `--dry-run` to validate configuration.
`KIGO_PUBLIC_BROWSER_SCENARIOS` selects `text`, `file`, or both; `KIGO_PUBLIC_BROWSER_TIMEOUT_SECONDS` controls
the per-scenario timeout. This test consumes TURN bandwidth and should use a quota-limited test deployment.

Native TCP relay smoke:

```sh
kigo relay --listen 127.0.0.1:9000 --room-ttl 10m --pass secret
kigo --relay 127.0.0.1:9000 --relay-pass secret send ./file-or-folder
kigo --relay 127.0.0.1:9000 --relay-pass secret recv <code> --out ./downloads
kigo --relay relay.example:9000 --proxy http://127.0.0.1:3128 send ./file-or-folder
```

`KIGO_RELAY` can provide the client endpoint instead of `--relay`; `KIGO_RELAY_PASS` can provide its password
instead of `--relay-pass`. Persist only the endpoint with `kigo config set relay ...`; passwords remain in a flag
or environment variable.
To make native/native selection automatic for clients using a signaling service, advertise the public relay:

```sh
kigo relay --listen :9000 --token-secret <shared-server-secret>
kigo serve --listen :9100 --native-relay relay.example.com:9000 \
  --native-relay-secret <shared-server-secret> \
  --native-relay-credential-ttl 2h
```

`--pass` remains available for older clients and explicitly configured relays. A relay configured with both
`--pass` and `--token-secret` accepts either the static password or a valid room-bound temporary credential.
`kigo relay` announces itself on `239.255.77.77:48765/udp` by default. While a native sender is pairing, it also
hosts an ephemeral relay and announces only its TCP port; the room token and pairing code are not broadcast.
Receivers collect LAN announcements first, race LAN rendezvous ahead of the explicit relay, and retain the
selected relay as fallback for direct TCP. `--local` on both clients now works without separately starting
`kigo relay`. Use `--no-lan` to disable sender hosting, discovery, and post-relay LAN upgrade, or
`--no-lan-announce` on a standalone relay to disable its announcements.

When both peers advertise `lan-upgrade-v1` but rendezvous still settles on an external relay, the receiver asks
the sender for a fresh LAN listener over that already paired relay pipe. The direct socket performs the normal
room-token and connection-index verification and may negotiate the same multi-connection bundle as other native
direct routes. Failure leaves the original relay untouched; older peers skip this phase.

Negotiated native clients try direct TCP first unless `--no-direct` or `--proxy` is set. Sender direct listening defaults to
`:0`; bidirectional-capable receivers now open the same reusable listener. Kigo advertises a non-loopback LAN
address when one is available through the signaling direct-rendezvous endpoint, then falls back to relay or
WebRTC if direct fails.
Use `--direct-listen` to force the listen address. `--direct-advertise` accepts one address or a
comma-separated candidate list; explicit entries are treated as manual high-priority choices. Wildcard
listeners advertise up to eight usable local IPv4/IPv6 addresses. Automatic races start global IPv6
immediately, LAN candidates after 40 milliseconds when a higher-priority route exists, and other candidates
after 100 milliseconds.
Use `--udp-probe` on both peers to enable NAT-aware timeout adaptation and authenticated UDP assistance for
signaling-direct and relay-rendezvous direct.
Use `--no-tcp-probe` to disable relay-observed same-port TCP mapping probes.
Windows uses `SO_REUSEADDR` for the reusable listener/dial pair, matching kiko. Unix platforms additionally use
`SO_REUSEPORT`. `doctor` reports this as `same-port=yes` and `doctor --json` exposes
`direct.same_port_supported`.
When talking to an older signaling service, clients with an explicit relay retain the previous relay-based
candidate exchange.

Relay fallback uses `--connections 4` by default, bounded to 1-8. Connection 0 carries handshake, manifest,
resume, and completion messages. Resume control is treated as a barrier: data messages that arrive first on
independent TCP connections are authenticated and deferred until the control response arrives. Stream lifecycle
messages retain a stable data connection, while file chunks
rotate across every negotiated data connection. The receiver writes authenticated chunks by offset, tracks
covered byte ranges, rejects overlap, and waits for full coverage even if `stream_end` or `done` arrives first.
Each data connection has a bounded sender worker; the scheduler uses pending bytes as live pressure and favors
paths that are draining faster. Sender logs report the bytes, chunks, and observed send rate for each active path.
Native direct TCP uses the same negotiated connection count and worker model after rendezvous. WebRTC remains
single-connection. Use `--connections 1` to disable physical mux.

Interrupted native relay and negotiated WebRTC transfers reuse the same pairing code and retry up to three total attempts by default.
File manifests are cached by the sender, matching completed files are skipped, and partial files resume from
their `.kigopart` size. A file manifest may carry `sample_sha256`: files up to 256 KiB are fully sampled, while
larger files hash bounded 128 KiB head/tail/interior regions and read at most 1 MiB. A size and sample match uses
the existing encrypted resume exchange with `skip=true, complete=true`; legacy manifests fall back to full
SHA-256 comparison. New peers negotiate `deferred-file-sha256`: the pairing code and sample manifest are ready
without a full source scan, and `resume_accept` supplies full SHA-256 only for files that were not skipped.
Older native/browser peers retain the original manifest SHA behavior. Files that actually transfer still
receive full SHA-256 verification before rename.
The receiver hashes a partial prefix; if it does not match the sender's source prefix, both sides agree to
restart that file from offset zero. Use `--no-reconnect`, `--reconnect-attempts`, and
`--reconnect-delay` to control retries.
WebRTC reconnect is enabled only when the signaling service negotiates `kigo-reconnect-v1`; older services and
clients retain the original one-shot room behavior.

Repeatable relay smoke:

```sh
./scripts/smoke_relay.sh
```

Interface-bound doctor, relay, and WebRTC smoke:

```sh
./scripts/smoke_interface.sh
```

The local web smoke script starts signaling plus the tokenized loopback console, validates API authorization, filesystem browsing, and
asset JavaScript, runs Doctor through the in-process task module, starts a send, verifies its public link, and
cancels it. It also exercises custom-pad local-Web/native persistent-notepad creation/opening, encrypted draft and service recovery, broadcast updates, clear, ACK,
and leave. The TUI smoke uses a Unix PTY to verify the menu, Doctor task, sender
pairing code/link, path browser, and a custom-pad persistent notepad with bidirectional edits, clear,
and leave. The relay smoke script builds a temporary `kigo`, starts a password-protected relay on
`127.0.0.1:19090`, and verifies signaling-only direct TCP, direct-to-relay fallback with temporary credentials,
NAT-aware direct timeout exchange and authenticated UDP assistance, direct-to-WebRTC fallback without a relay, four-connection single-file chunk
striping, synchronized bidirectional direct negotiation through signaling and relay rendezvous, native text transfer,
resume from an existing `.kigopart`, legacy relay-rendezvous direct selection, persisted path-weight loading
and weighted chunk distribution, history-driven peer route negotiation, v1-to-v2 history migration, and
wrong-password rejection.

## Release builds

Print local build metadata:

```sh
kigo version
kigo version --json
```

Build cross-platform release archives:

```sh
KIGO_VERSION=v0.1.0 ./scripts/build_release.sh
./scripts/verify_release.sh dist v0.1.0
```

Development builds default to the loopback service. A public distribution can inject its default signaling
and share-link origin without hard-coding deployment infrastructure in source:

```sh
KIGO_VERSION=v0.1.0 \
KIGO_DEFAULT_SERVICE_URL=https://kigo.example \
  ./scripts/build_release.sh
```

Explicit flags, environment variables, and saved client configuration still take precedence over the injected
default. The build URL must use HTTP(S) and cannot contain credentials, a query, fragment, whitespace, or a
trailing slash.

The release script builds:

- `darwin/amd64`
- `darwin/arm64`
- `linux/amd64`
- `linux/arm64`
- `windows/amd64`

Artifacts are written to `dist/` with:

- one `.tar.gz` or `.zip` archive per target
- `kigo-v0.1.0.cdx.json`, a CycloneDX 1.6 dependency SBOM
- `SHA256SUMS`, covering all five archives and the SBOM

The build script pins `cyclonedx-gomod` for SBOM generation and injects version, commit, and UTC build date into
`kigo version` using Go linker flags. `verify_release.sh` checks every digest, expected archive members, the
CycloneDX document shape, and the embedded version/platform metadata for the current host binary.

CI runs formatting, module consistency, unit, race, vet, native/Linux smoke, Chromium WebRTC smoke, container
build, and release-layout checks. A pushed semantic version tag creates the GitHub release:

```sh
git tag -a v0.1.0 -m "Kigo v0.1.0"
git push origin v0.1.0
```

Public tag builds require the repository Actions variable `KIGO_DEFAULT_SERVICE_URL`. The release workflow
validates it and passes it to `build_release.sh`, so downloaded clients use the deployed public service without
manual configuration. Configure it before pushing a release tag:

```sh
gh variable set KIGO_DEFAULT_SERVICE_URL --body https://kigo.example
```

The release workflow builds from that exact tag, publishes all files in `dist/`, and creates GitHub keyless
build-provenance plus SBOM attestations for the archives. After downloading a release, verify it with:

```sh
sha256sum -c SHA256SUMS
gh attestation verify kigo-v0.1.0-linux-amd64.tar.gz --repo suir1/kigo
```

On macOS, use `shasum -a 256 -c SHA256SUMS` for the checksum step.

## Deploy

For public deployment, terminate TLS directly with:

```sh
kigo serve --listen :443 --tls-cert cert.pem --tls-key key.pem --public-url https://kigo.example
```

or run behind a reverse proxy that provides HTTPS.

Validate environment variables, public URL, and a direct TLS certificate/key pair without binding ports or
starting TURN:

```sh
kigo serve --check-config
```

The provided systemd service runs this check through `ExecStartPre` before each service start.

Browsers require a secure context for the full file-transfer UI on public origins, so production web deployments should use HTTPS. TURN needs UDP reachability on the advertised port.

When a reverse proxy is used, configure only its address with `--trusted-proxies` or
`KIGO_TRUSTED_PROXIES`. Kigo then walks `X-Forwarded-For` from the trusted hop toward the client and keeps
signaling and TURN-credential rate limits separate per public source IP. Forwarding headers from untrusted
direct connections are ignored.

For a simple self-hosted TURN fallback:

```sh
kigo serve --listen :443 --tls-cert cert.pem --tls-key key.pem --public-url https://kigo.example \
  --turn-listen 0.0.0.0:3478 --turn-public-ip <public-ip> \
  --turn-min-port 49160 --turn-max-port 50159 \
  --turn-secret <shared-secret> --turn-credential-ttl 2h
```

`--turn-secret` enables TURN REST credentials whose username contains an expiry timestamp and random client
identifier. The password is the standard HMAC-SHA1 TURN REST credential derived from the shared secret. These
credentials work with the built-in Pion TURN server and with external coturn configured with the same
`static-auth-secret`. Without `--turn-secret`, `--turn-user` and `--turn-pass` retain the previous static
credential behavior.

The TURN control socket uses UDP `3478`; each allocation also needs a UDP relay socket. Without
`--turn-min-port` and `--turn-max-port`, the operating system chooses dynamic relay ports. Container and
firewall deployments should configure and expose the same bounded range. Range endpoints must be configured
together, and the maximum is `65534`.

The built-in server defaults to 1024 active allocations globally, 4 per temporary credential, and 32 per
source IP. Use `--turn-max-allocations`, `--turn-max-allocations-per-user`, and
`--turn-max-allocations-per-ip` to tune them; `-1` disables an individual ceiling. `/api/ice` credential
responses are `no-store` and limited by `--turn-credentials-per-minute`.
Signaling, transport negotiation, and direct rendezvous share a per-source-IP
limit controlled by `--signal-requests-per-minute` (default 60).

Built-in TURN also counts UDP bytes actually written by the server, including relayed payload and TURN
framing. `--turn-max-egress-mib`, `--turn-max-egress-mib-per-user`, and
`--turn-max-egress-mib-per-ip` enable token-bucket limits; all three default to `-1` (accounting only, no byte
ceiling). Bucket capacity is the configured MiB value and it refills continuously over
`--turn-egress-window`, which defaults to one hour. Packets above the available bucket balance are dropped
without closing the allocation, allowing WebRTC retransmission to recover as capacity refills. These controls
require `--turn-listen`; external coturn traffic must be metered and limited by coturn or the hosting provider.

Deployment flags can also come from environment variables. Explicit CLI flags win over env values.

```sh
KIGO_LISTEN=:443
KIGO_PUBLIC_URL=https://kigo.example
KIGO_NATIVE_RELAY=relay.example.com:9000
KIGO_NATIVE_RELAY_SECRET=<shared-native-relay-secret>
KIGO_NATIVE_RELAY_CREDENTIAL_TTL=2h
KIGO_SIGNAL_REQUESTS_PER_MINUTE=60
KIGO_NOTE_STORE=/var/lib/kigo/notes
KIGO_NOTE_TTL=720h
KIGO_TLS_CERT=/etc/kigo/cert.pem
KIGO_TLS_KEY=/etc/kigo/key.pem
KIGO_TURN_LISTEN=0.0.0.0:3478
KIGO_TURN_PUBLIC_IP=<public-ip>
KIGO_TURN_USER=kigo
KIGO_TURN_SECRET=<shared-secret>
KIGO_TURN_REALM=kigo
KIGO_TURN_CREDENTIAL_TTL=2h
KIGO_TURN_CREDENTIALS_PER_MINUTE=1200
KIGO_TURN_MIN_PORT=49160
KIGO_TURN_MAX_PORT=50159
KIGO_TURN_MAX_ALLOCATIONS=512
KIGO_TURN_MAX_ALLOCATIONS_PER_USER=4
KIGO_TURN_MAX_ALLOCATIONS_PER_IP=32
KIGO_TURN_EGRESS_WINDOW=1h
KIGO_TURN_MAX_EGRESS_MIB=-1
KIGO_TURN_MAX_EGRESS_MIB_PER_USER=-1
KIGO_TURN_MAX_EGRESS_MIB_PER_IP=-1
KIGO_TRUSTED_PROXIES=127.0.0.1/32
kigo serve
```

`kigo serve` also accepts `KIGO_WEB_DIR`, `KIGO_NOTE_STORE`, `KIGO_NOTE_TTL`, `KIGO_NATIVE_RELAY`,
`KIGO_NATIVE_RELAY_SECRET`, `KIGO_NATIVE_RELAY_CREDENTIAL_TTL`, and `KIGO_TURN` for an externally managed TURN URL. Static TURN
deployments may continue using `KIGO_TURN_PASS`. `kigo relay` accepts `KIGO_RELAY_LISTEN`,
`KIGO_RELAY_ROOM_TTL`, `KIGO_RELAY_PASS`, and `KIGO_RELAY_TOKEN_SECRET`; native relay clients also use
`KIGO_RELAY_PASS` when `--relay-pass` is omitted.

Docker Compose:

```sh
cp deploy/docker-compose.yml deploy/docker-compose.local.yml
# edit KIGO_PUBLIC_URL, KIGO_TURN_PUBLIC_IP, and secrets
docker compose -f deploy/docker-compose.local.yml up -d --build
```

The development Compose example exposes web/signaling on `9100/tcp`, built-in TURN on `3478/udp` plus
`49160-50159/udp`, and the native relay on `9000/tcp`. Set `KIGO_NATIVE_RELAY` to the relay's publicly reachable host and port; the signaling container
uses it only for capability negotiation. `KIGO_NATIVE_RELAY_SECRET` and `KIGO_RELAY_TOKEN_SECRET` must contain
the same deployment secret. Put an HTTPS reverse proxy in front of `9100`, or change the service env to use
`KIGO_TLS_CERT` and `KIGO_TLS_KEY`.

For a public single-host deployment, use the production Compose stack with Caddy-managed HTTPS:

```sh
cp deploy/production.env.example deploy/production.env
# Set the real domain/public IPv4 and generate both secrets with: openssl rand -hex 32
./scripts/deploy_preflight.sh --env-file deploy/production.env
./scripts/deploy_preflight.sh --env-file deploy/production.env --runtime
docker compose --env-file deploy/production.env \
  -f deploy/compose.production.yml up -d
```

Before starting, point the domain's DNS records at the host and allow inbound `80/tcp`, `443/tcp`, optional
`443/udp` for HTTP/3, `3478/udp`, `49160-50159/udp`, and `9000/tcp`. The stack does not publish Kigo's plain
HTTP port. Caddy has a fixed address on the private `172.30.77.0/24` Compose network, and Kigo trusts only
that address for forwarding headers. Change the subnet and all three container IP overrides together if it
conflicts with an existing network.

The preflight rejects documentation domains, reserved example IPs, and placeholder/short secrets. With
`--runtime`, it additionally builds the Kigo image, runs `serve --check-config`, verifies the image healthcheck
dependency, and validates the Caddyfile without starting the stack.

Systemd:

```sh
go build -o ./kigo ./cmd/kigo
sudo useradd --system --home-dir /var/lib/kigo --shell /usr/sbin/nologin kigo
sudo install -m 0755 ./kigo /usr/local/bin/kigo
sudo install -d /etc/kigo
sudo install -d -o kigo -g kigo /var/lib/kigo
sudo install -m 0640 -o root -g root deploy/kigo.env.example /etc/kigo/kigo.env
sudo install -m 0644 deploy/kigo.service /etc/systemd/system/kigo.service
sudo systemctl daemon-reload
sudo systemctl enable --now kigo
```

For the native TCP relay, copy `deploy/kigo-relay.env.example` to `/etc/kigo/kigo-relay.env`, install `deploy/kigo-relay.service`, then enable `kigo-relay`.
