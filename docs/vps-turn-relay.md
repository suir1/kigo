# VPS public relays

Kigo's persistent native TCP and WebRTC TURN relays are deployed on the
`kiko_vps` SSH host.

## Endpoints

- Web app and signaling: `https://106.53.170.243:1001` on `1001/tcp`
- Native TCP relay: `106.53.170.243:5140` on `5140/tcp`
- Built-in TURN: `turn:106.53.170.243:5140` on `5140/udp`
- TURN allocation range: `49160-49259/udp`
- systemd units: `kigo-public.service` and `kigo-relay.service`

TCP and UDP use separate port spaces, so both relay protocols can use port 5140
without conflict. The signaling service advertises the native relay and issues
room-bound temporary credentials. Clients obtain the TURN URL and its temporary
credentials from `/api/ice` automatically.

Clients configured with this signaling service do not need to save an explicit
relay endpoint. Native-native negotiation receives the TCP relay dynamically,
while native/browser and browser/browser sessions receive TURN through ICE.

## Client setup

The IP-only service currently uses a self-signed certificate. Copy its public
certificate to a stable local path and save both settings:

```sh
scp kiko_vps:/etc/kigo/tls/server.crt "$HOME/Library/Application Support/kigo/vps-ca.crt"
kigo config set service https://106.53.170.243:1001
kigo config set tls-ca "$HOME/Library/Application Support/kigo/vps-ca.crt"
kigo route --pair native-web --json
```

The current certificate has SHA-256 fingerprint
`6F:AB:48:3A:B4:71:64:0B:BF:82:BB:00:58:7F:E6:FE:43:C9:01:90:41:BC:D3:12:57:CA:22:B1:D2:99:80:A4`
and expires on 2027-07-20. Verify the fingerprint over a trusted channel before
installing the certificate on another device.

Browsers do not use Kigo's saved CA path. Until the service has a domain and a
publicly trusted certificate, browser users must explicitly trust or proceed
past the self-signed certificate warning.

## Operations

```sh
ssh kiko_vps 'systemctl status kigo-public.service'
ssh kiko_vps 'systemctl status kigo-relay.service'
ssh kiko_vps 'journalctl -u kigo-public.service -n 100 --no-pager'
ssh kiko_vps 'journalctl -u kigo-relay.service -n 100 --no-pager'
ssh kiko_vps 'systemctl restart kigo-relay.service kigo-public.service'
```

Server files:

- `/usr/local/bin/kigo`
- `/etc/systemd/system/kigo-public.service`
- `/etc/systemd/system/kigo-relay.service`
- `/etc/kigo/kigo.env`
- `/etc/kigo/kigo-relay.env`
- `/etc/kigo/tls/server.crt`
- `/etc/kigo/tls/server.key`

The environment files are readable only by root and the `kigo` group. They
contain matching native relay token secrets and the TURN credential secret and
must not be copied into test artifacts or source control.

The service currently limits TURN to 128 active allocations globally, 4 per
temporary credential, and 16 per source IP. Egress token buckets allow 10 GiB
globally, 2 GiB per credential, and 4 GiB per source IP per one-hour window.

## Verification

The persistent service was verified on 2026-07-20 with forced relay-only ICE.
Chromium transferred encrypted text and a random 256 KiB file with matching
SHA-256 checksums. Both peers selected `relay/udp`; service health reported zero
dropped bytes and zero quota failures. Evidence is stored in
`artifacts/vps-public-web-1001-chromium/matrix.json`.

A native-native file run also disabled direct TCP and LAN discovery, negotiated
`service-native-relay` through signaling, received temporary room-bound relay
credentials, and completed over four TCP relay connections with a matching
file checksum.

The service binary was upgraded on 2026-07-22 to `v0.1.0-dev.20260722`
(`commit: workspace`, build time `2026-07-22T03:04:20Z`). Post-deploy verification repeated both paths:

- Chromium Web-Web forced TURN transferred encrypted text and a file successfully. Evidence is in
  `artifacts/vps-postdeploy-20260722/matrix.json`.
- Native-Native disabled direct TCP and LAN discovery, negotiated the service relay with a temporary
  credential, used four TCP relay connections, and transferred a random 1 MiB file with matching SHA-256.

The browser ICE fallback fix was deployed later on 2026-07-22 as
`v0.1.0-dev.20260722.icefix1` (build time `2026-07-22T06:54:46Z`, SHA-256
`d005a7ff0bc39071e93e2fda32aa868f01c3cf3470373e9eaa690ad8c4caad47`). The previous binary is retained at
`/usr/local/bin/kigo.backup-20260722-1455`. Web-Web now closes a timed-out direct-only peer connection on both
roles before reconnecting with relay-only ICE; it no longer injects delayed TURN candidates into a stalled
peer connection.

Post-fix Chromium checks passed encrypted text and file transfer in both natural ICE and forced-TURN modes.
The natural run exercised direct failure followed by `relay/relay` UDP fallback. Evidence is stored in
`artifacts/vps-postfix-20260722-natural/matrix.json` and
`artifacts/vps-postfix-20260722-forced-turn/matrix.json`. Service health afterward reported zero active TURN
allocations, dropped bytes, and quota failures.

The service was then upgraded to `v0.1.0-dev.20260722.icefix2` (build time
`2026-07-22T07:20:26Z`, SHA-256
`b55c035145767364e15974cd9a8cd76667208516d615f63e54afc206980495cd`). The `icefix1` binary is retained at
`/usr/local/bin/kigo.backup-20260722-1521-icefix1`. `/api/ice` now advertises
`stun:106.53.170.243:5140` before the public Google STUN servers, using the verified STUN support on Kigo's
built-in TURN listener. Browser direct attempts now allow six seconds and log local/remote candidate types
before relay fallback without logging candidate addresses.

After `icefix2`, the same Chromium natural-ICE test changed from `relay/relay` fallback to direct
`srflx/srflx` UDP for both encrypted text and file transfer. Forced-TURN file transfer remained successful.
Evidence is stored in `artifacts/vps-postfix2-20260722-natural/matrix.json` and
`artifacts/vps-postfix2-20260722-forced-turn/matrix.json`.

`v0.1.0-dev.20260722.icefix3` was deployed on 2026-07-22 (build time
`2026-07-22T08:11:33Z`, SHA-256
`722411e68d8faff879440ad113eca7f63c45a0ee64fb58e8b865bdd9ed774aa2`). The `icefix2` rollback binary is
`/usr/local/bin/kigo.backup-20260722-1612-icefix2`. Web-Web now attempts host-only LAN ICE before creating a
fresh STUN peer connection and finally a fresh relay-only connection. This prevents a quickly nominated but
slow NAT-hairpin `srflx` pair from preempting a viable `host/host` LAN pair. Host paths retain 192 KiB chunks;
STUN and TURN paths negotiate 64 KiB chunks to reduce SCTP fragmentation and head-of-line delay.

The post-deploy natural matrix passed text over direct `srflx/srflx` and verified automatic file fallback to
`relay/relay`; the forced-TURN file matrix also passed. Evidence is stored in
`artifacts/vps-postfix3-20260722-natural/matrix.json` and
`artifacts/vps-postfix3-20260722-forced-turn/matrix.json`.

`v0.1.0-dev.20260722.icefix4` was deployed on 2026-07-22 (build time
`2026-07-22T09:27:54Z`, SHA-256
`f3b4ec583a639fdd1e161c841a802864165b89d23f0f67acd5e36eb028a456ce`). The `icefix3` rollback binary is
`/usr/local/bin/kigo.backup-20260722-1728-icefix3`. Web-Web route negotiation now enables
`unordered-data-v1` only when both peers advertise support. Negotiated peers keep manifest and control frames
on the ordered channel and send encrypted chunks over a reliable-unordered channel; a wire-envelope reorder
buffer restores global sequence before decryption. Mixed-version and Native-Web peers retain the ordered
single-channel path.

The browser suite includes a forced out-of-order file test that delays the first data frame, receives later
frames first, and requires a matching downloaded SHA-256. Post-deploy natural text/file, explicit unordered
feature negotiation, and forced-TURN file checks passed. Evidence is stored in
`artifacts/vps-postfix4-20260722-natural/matrix.json` and
`artifacts/vps-postfix4-20260722-forced-turn/matrix.json`.

The same 68 MiB host-path transfer measured 5.19 MB/s with the ordered baseline and 4.23 MB/s with unordered
data enabled. `v0.1.0-dev.20260722.icefix5` therefore disables unordered negotiation by default while retaining
the implementation and forced-reordering test as an experimental capability. It was deployed with build time
`2026-07-22T10:00:34Z` and SHA-256
`de56863300332e4ed149ab7fb9007823af254bea31751f8ef97c79a8000be291`; the `icefix4` rollback binary is
`/usr/local/bin/kigo.backup-20260722-1801-icefix4`. The post-deploy natural file check passed and is recorded in
`artifacts/vps-postfix5-20260722-natural/matrix.json`.

`v0.1.0-dev.20260723.parallel1` was deployed on 2026-07-23 (build time
`2026-07-23T07:07:47Z`, SHA-256
`c032bda7342f4322db63deb9d5be4a2abacf70d1a9f84d13c32f75286d3cd717`). It keeps the ordered single
DataChannel as the default and advertises `parallel-data-v1` only when both browser pages are opened with
`?parallel=1`. The experimental direct path uses one control PeerConnection plus two independent file-data
PeerConnections, restores global envelope order before decryption, and automatically returns to one connection
for TURN fallback. The `icefix5` rollback binary is
`/usr/local/bin/kigo.backup-20260723-1508-icefix5`. The default forced-TURN public text/file matrix passed and is
recorded in `artifacts/vps-parallel1-default-20260723/matrix.json`.

`v0.1.0-dev.20260723.parallel2` was deployed later on 2026-07-23 (build time
`2026-07-23T07:27:09Z`, SHA-256
`34ae8308281bdbb26f8e87c41b56348483040bd51df88b1795b1b786d08633a5`). Parallel data paths are now
opportunistic: failure of an auxiliary PeerConnection closes the partial lane set but preserves the already-open
direct control connection as the single transfer path instead of restarting ICE and unnecessarily falling back to
TURN. The `parallel1` rollback binary is `/usr/local/bin/kigo.backup-20260723-1528-parallel1`. A forced lane
failure browser regression, the full native/web smoke suite, and the default public text/file matrix all passed;
the public matrix is recorded in `artifacts/vps-parallel2-default-20260723/matrix.json`.

## Public certificate status

The service remains on the self-signed IP certificate described above. On 2026-07-22, ACME validation was
tested with `106-53-170-243.sslip.io`, `106-53-170-243.nip.io`, and `106.53.170.243.nip.io`. The hyphenated
aliases were redirected to a DNSPod block page, while Let's Encrypt connections to the otherwise-correct IP
were reset by the cloud network on both HTTP-01 and TLS-ALPN-01. No certificate was issued and the temporary
ACME client and cron entry were removed.

Do not inject one of these aliases as a production client default until it has a trusted certificate and a
fresh public browser matrix. Release builds can inject a future canonical origin without changing source:

```sh
KIGO_DEFAULT_SERVICE_URL=https://kigo.example \
KIGO_VERSION=v0.1.0 \
  ./scripts/build_release.sh
```
