# Archived network matrix baselines: 2026-07

These results are retained as historical implementation evidence. They are not
current release acceptance and their ignored local `artifacts/` files may no
longer exist.

## Local baseline: 2026-07-17

| Engine/profile | Result | Evidence |
| --- | --- | --- |
| Chrome combined | Pass | Native/web and web/web core matrix; full Chromium smoke also passed |
| Playwright WebKit native/web | Pass | Physical-interface profile, same-machine TURN disabled |
| Playwright WebKit web/web | Pass | Built-in TURN profile |
| Playwright Firefox protocol guards | Pass | WebCrypto, transfer protocol, compression, mux, and validation guards |
| Playwright Firefox transfer | Environment blocked locally | Headless runtime exposed only the active TUN, which could not hairpin; no external TURN was configured |

## Public TURN baseline: 2026-07-17

The ephemeral IP-only VPS used TURN control port `5140/udp` and relay ports
`49160-49259/udp`.

| Engine/profile | Result | Evidence |
| --- | --- | --- |
| Chromium forced TURN | Pass | Relay/UDP text and random 256 KiB file with SHA-256 verification |
| Firefox forced TURN | Pass | Relay/UDP text and random 256 KiB file with SHA-256 verification |
| Playwright WebKit forced TURN | Pass | Relay/UDP text and random 256 KiB file with SHA-256 verification |
| Native/browser external service | Pass | Native-to-web and web-to-native file and text scenarios |

The original sanitized evidence lived under `artifacts/vps-turn-5140-*`. Service
health after the run reported no dropped bytes, quota failures, or active TURN
allocations. Real Safari and mobile-network coverage remained release gaps.

## Persistent endpoint: 2026-07-20

The endpoint was installed as `kigo-public.service` with public web/signaling on
`1001/tcp`, native TCP relay on `5140/tcp`, and built-in TURN on `5140/udp`. A
Chromium relay-only text/file run and forced native-native fallback both passed.
The original browser result was recorded as
`artifacts/vps-public-web-1001-chromium/matrix.json`.
