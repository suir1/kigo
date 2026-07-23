# Network matrix

Kigo has two network test layers. They answer different questions and their
results must not be interpreted as equivalent.

It also has a browser-engine matrix for compatibility testing. That matrix is
described separately because localhost WebRTC topology is not evidence about
public NAT traversal.

## Linux namespace lab

`scripts/nat_lab.sh` creates five Linux network namespaces:

```text
sender -> router 1 -> WAN signaling/relay <- router 2 <- receiver
```

It runs these deterministic scenarios:

| Scenario | Model | Required result |
| --- | --- | --- |
| `routed-open` | Routed IPv4 endpoint networks | Native direct TCP |
| `port-preserving-nat` | Separate SNAT routers with fixed inbound port mappings | Native direct TCP through relay-observed candidates |
| `relay-fallback` | Separate outbound-only SNAT routers | Native relay after direct failure |
| `ipv6-routed` | Routed IPv6 ULA endpoint networks | Native direct TCP, skipped if IPv6 is unavailable |

Inspect the plan without root or Linux:

```sh
./scripts/nat_lab.sh --dry-run
```

Run it on Linux:

```sh
go build -o /tmp/kigo ./cmd/kigo
sudo env \
  KIGO_BIN=/tmp/kigo \
  KIGO_ARTIFACT_DIR="$PWD/artifacts/nat-lab" \
  ./scripts/nat_lab.sh
```

The script writes `matrix.json`, per-scenario summaries, redacted send/receive
logs, `doctor --json`, and `route --json`. A scenario passes only when both
processes exit successfully, the received SHA-256 matches, and the selected
route matches the expected route.

The namespace lab is not a CGNAT emulator. The mapped NAT case deliberately
uses fixed DNAT rules, and the outbound-only case uses ordinary Linux
conntrack. These cases catch route negotiation, same-port probing, direct
handshake, and relay fallback regressions; they do not prove behavior on a
specific ISP or enterprise firewall.

## Public endpoint matrix

`scripts/public_matrix.sh` controls two existing POSIX endpoints over SSH.
Both endpoints need `bash`, Kigo, and either `sha256sum` or `shasum`. The
controller needs SSH and Python 3. The signaling service and optional relay
must already be publicly reachable.

Validate configuration without connecting:

```sh
KIGO_SENDER_HOST=user@sender.example \
KIGO_RECEIVER_HOST=user@receiver.example \
KIGO_SIGNAL_URL=https://kigo.example \
KIGO_RELAY_ENDPOINT=relay.example:9000 \
./scripts/public_matrix.sh --dry-run
```

Run one observation:

```sh
KIGO_SENDER_HOST=user@sender.example \
KIGO_RECEIVER_HOST=user@receiver.example \
KIGO_SENDER_LABEL=residential-fiber \
KIGO_RECEIVER_LABEL=mobile-hotspot \
KIGO_SIGNAL_URL=https://kigo.example \
KIGO_RELAY_ENDPOINT=relay.example:9000 \
KIGO_PUBLIC_EXPECT_ROUTE=direct \
KIGO_ARTIFACT_DIR="$PWD/artifacts/public-matrix" \
./scripts/public_matrix.sh
```

Prefer a signaling service that advertises temporary relay credentials. Set
`KIGO_RELAY_PASS` only for a separately managed relay that requires a static
password. Passwords, pairing codes, room tokens, TURN usernames, and
credential fields are removed from generated artifacts.

Useful controls:

| Variable | Default | Meaning |
| --- | --- | --- |
| `KIGO_REMOTE_BIN` | `kigo` | Remote Kigo executable |
| `KIGO_PUBLIC_PAYLOAD_BYTES` | `1048576` | Random test payload size |
| `KIGO_PUBLIC_TIMEOUT_SECONDS` | `90` | Per-peer transfer timeout |
| `KIGO_PUBLIC_EXPECT_ROUTE` | `any` | `any`, `direct`, `relay`, or `webrtc` |
| `KIGO_PUBLIC_TRANSPORT` | `auto` | Kigo transport policy |
| `KIGO_PUBLIC_DIRECT_TIMEOUT` | `2s` | Native direct attempt timeout |
| `KIGO_PUBLIC_UDP_PROBE` | `0` | Set to `1` to exchange STUN mapping classes |

Archive `matrix.json` together with its sibling files. Record endpoint labels,
access technology, VPN state, and approximate geography in external test-run
metadata. Do not infer a NAT type only from a successful direct connection:
the `doctor` STUN classification is a UDP mapping heuristic, while Kigo direct
uses TCP.

## Browser engine matrix

`scripts/smoke_browser_matrix.sh` runs a compact native/web and web/web suite
through Playwright Chromium, Firefox, and WebKit:

```sh
KIGO_BROWSER_MATRIX=chromium,firefox,webkit \
  ./scripts/smoke_browser_matrix.sh
```

On a machine with an active VPN/TUN, native Pion and the browser may select
different local interfaces. Bind the native side to the physical interface
and split native/web from web/web local TURN profiles:

```sh
KIGO_BROWSER_MATRIX=webkit \
KIGO_BROWSER_MATRIX_NATIVE_INTERFACE=en0 \
./scripts/smoke_browser_matrix.sh
```

The failure dump includes signaling, ICE gathering, ICE connection, peer
connection, redacted SDP capability, candidate-type counts, and candidate-pair
states. It intentionally omits candidate addresses. A browser engine passing
on localhost validates WebRTC/DataChannel and Kigo application-protocol
compatibility, not CGNAT traversal.

Playwright WebKit is not released Safari. Release acceptance still requires
Safari on macOS and iOS plus Firefox on a host where its selected interface can
reach an external TURN endpoint. A headless Firefox run that exposes only a
non-hairpin VPN/TUN candidate is an environment-blocked result, not a product
pass or fail.

Local baseline recorded on 2026-07-17:

| Engine/profile | Result | Evidence |
| --- | --- | --- |
| Chrome combined | Pass | Native/web and web/web core matrix; full Chromium smoke also passed |
| Playwright WebKit native/web | Pass | Physical-interface profile, same-machine TURN disabled |
| Playwright WebKit web/web | Pass | Built-in TURN profile |
| Playwright Firefox protocol guards | Pass | WebCrypto, transfer protocol, compression, mux, and validation guards |
| Playwright Firefox transfer | Environment blocked locally | Headless runtime exposed only the active TUN, which could not hairpin; no external TURN was configured |

Public TURN baseline recorded on 2026-07-17 against an ephemeral IP-only VPS
deployment using TURN control port `5140/udp` and relay ports
`49160-49259/udp`:

| Engine/profile | Result | Evidence |
| --- | --- | --- |
| Chromium forced TURN | Pass | Relay/UDP text and random 256 KiB file with SHA-256 verification |
| Firefox forced TURN | Pass | Relay/UDP text and random 256 KiB file with SHA-256 verification |
| Playwright WebKit forced TURN | Pass | Relay/UDP text and random 256 KiB file with SHA-256 verification |
| Native/browser external service | Pass | Native-to-web and web-to-native file and text scenarios |

The public browser artifacts are under `artifacts/vps-turn-5140-*`. They contain
sanitized ICE and checksum evidence and no pairing codes, credentials, candidate
addresses, or payloads. Service health after the run reported zero dropped
bytes, zero quota failures, and no active TURN allocations. This replaces the
local Firefox environment block for protocol and public TURN compatibility;
real Safari and mobile-network coverage remain release gaps.

The same endpoint was installed as the persistent `kigo-public.service` on
2026-07-20. Its public web and signaling origin now uses port `1001/tcp` and
passed a fresh Chromium relay-only text and file run. See
`docs/vps-turn-relay.md` and
`artifacts/vps-public-web-1001-chromium/matrix.json`.

The persistent endpoint also advertises a Kigo native TCP relay on `5140/tcp`.
A forced native-native fallback run negotiated temporary room-bound credentials,
used four relay connections, and completed with a matching file checksum.

### Public TURN browser run

Use `scripts/smoke_public_browser.sh` against a deployed Kigo service to remove
the loopback-TURN ambiguity from Firefox/WebKit testing:

```sh
KIGO_PUBLIC_BROWSER_URL=https://kigo.example \
KIGO_PUBLIC_BROWSER_ENGINE=firefox \
KIGO_ARTIFACT_DIR="$PWD/artifacts/public-browser-firefox" \
./scripts/smoke_public_browser.sh --dry-run

KIGO_PUBLIC_BROWSER_URL=https://kigo.example \
KIGO_PUBLIC_BROWSER_ENGINE=firefox \
KIGO_ARTIFACT_DIR="$PWD/artifacts/public-browser-firefox" \
./scripts/smoke_public_browser.sh
```

For an ephemeral IP-only test deployment with a self-signed certificate, set
`KIGO_PUBLIC_BROWSER_IGNORE_TLS_ERRORS=1`. This disables certificate validation
only inside the test runner and is not a production deployment mode.

The runner forces relay-only ICE, requires `/api/ice` to advertise TURN,
transfers text and a random 256 KiB file, verifies the download checksum, and
asserts that the selected local candidate type is `relay`. The generated
`matrix.json` excludes pairing codes, room tokens, candidate addresses, TURN
credentials, and payload contents. Failed scenarios retain address-free ICE
states, candidate-type counts, and sanitized browser logs.

This is a TURN and browser-protocol observation from one client network. It
does not replace the SSH public endpoint matrix, mobile Safari validation, or
testing from two independent access networks.

For the bundled production Compose stack, both UDP `3478` and the configured
TURN relay range (default `49160-50159`) must be reachable from the browser
network. A successful `/api/ice` response proves credential issuance only; the
runner's selected `relay` candidate assertion proves that an allocation and its
relay socket were actually usable.
