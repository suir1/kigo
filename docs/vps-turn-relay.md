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

The IP-only service uses a publicly trusted Let's Encrypt IP address
certificate. Native clients only need to save the service URL; browsers can
open the same URL without installing a CA or bypassing a certificate warning:

```sh
kigo config set service https://106.53.170.243:1001
kigo config unset tls-ca
kigo route --pair native-web --json
```

The certificate contains the critical IP SAN `106.53.170.243`, is issued by
Let's Encrypt `YE1`, and has SHA-256 fingerprint
`88:3A:FB:AC:01:4F:DC:28:A1:56:21:89:AF:06:BF:67:5D:CF:14:79:B9:32:AD:5F:FE:E3:9E:7A:15:C6:09:C0`.
IP certificates are intentionally short-lived, so the fingerprint and expiry
change automatically on renewal.

## Operations

```sh
ssh kiko_vps 'systemctl status kigo-public.service'
ssh kiko_vps 'systemctl status kigo-relay.service'
ssh kiko_vps 'journalctl -u kigo-public.service -n 100 --no-pager'
ssh kiko_vps 'journalctl -u kigo-relay.service -n 100 --no-pager'
ssh kiko_vps 'systemctl restart kigo-relay.service kigo-public.service'
ssh kiko_vps 'systemctl list-timers kigo-certbot-renew.timer --no-pager'
ssh kiko_vps 'journalctl -u kigo-certbot-renew.service -n 100 --no-pager'
```

Server files:

- `/usr/local/bin/kigo`
- `/etc/systemd/system/kigo-public.service`
- `/etc/systemd/system/kigo-relay.service`
- `/etc/kigo/kigo.env`
- `/etc/kigo/kigo-relay.env`
- `/etc/kigo/tls/server.crt`
- `/etc/kigo/tls/server.key`
- `/opt/certbot/`
- `/etc/letsencrypt/live/106.53.170.243/`
- `/etc/letsencrypt/renewal-hooks/deploy/kigo-public`
- `/etc/systemd/system/kigo-certbot-renew.service`
- `/etc/systemd/system/kigo-certbot-renew.timer`

The environment files are readable only by root and the `kigo` group. They
contain matching native relay token secrets and the TURN credential secret and
must not be copied into test artifacts or source control.

The service currently limits TURN to 128 active allocations globally, 4 per
temporary credential, and 16 per source IP. Egress token buckets allow 10 GiB
globally, 2 GiB per credential, and 4 GiB per source IP per one-hour window.

## Current deployment

The VPS is running [`v0.1.0-alpha.4`](https://github.com/suir1/kigo/releases/tag/v0.1.0-alpha.4),
deployed on 2026-07-29 from merge commit
`12cf5269543da5471cf8e01a94274b872beae940`. `kigo version --json` reports
Go 1.26.5 and Linux amd64. The deployed `/usr/local/bin/kigo` SHA-256 is
`ae5dbd9c4de86944448fd93b3131970f9d187b928bb0bf087d0ef884606c3663`.
The no-version installer selects this same release and verifies its published
archive checksum before installation.

Both `kigo-public.service` and `kigo-relay.service` load this binary. The public
service uses `KIGO_NOTE_STORE=/var/lib/kigo/notes` and `KIGO_NOTE_TTL=720h`.
The notes directory is owned by `kigo:kigo` with mode `0700`; encrypted snapshot
files use mode `0600`. The service unit also declares `StateDirectory=kigo` and
retains `ReadWritePaths=/var/lib/kigo` under `ProtectSystem=strict`.

The immediately previous `v0.1.0-alpha.3` binary is retained at
`/usr/local/bin/kigo.backup-20260729-084644-alpha3`. To roll back both public
services while preserving encrypted snapshots:

```sh
ssh kiko_vps 'sudo install -m 0755 /usr/local/bin/kigo.backup-20260729-084644-alpha3 /usr/local/bin/kigo && sudo systemctl restart kigo-relay.service kigo-public.service'
ssh kiko_vps '/usr/local/bin/kigo version --json && systemctl is-active kigo-relay.service kigo-public.service'
```

## Verification

The `v0.1.0-alpha.4` deployment passed these checks on 2026-07-29:

- Go was updated from 1.23.1 to 1.26.5, `golang.org/x/net` to v0.57.0,
  `golang.org/x/crypto` to v0.54.0, the Docker builder to Go 1.26.5, and the
  runtime image to Alpine 3.24. Source and unstripped candidate-binary
  `govulncheck` scans reported zero reachable vulnerabilities.
- Pull request 19 passed all eight CI jobs. The release workflow repeated the
  vulnerability gate, Windows direct tests, source tests, five-platform build,
  archive verification, SBOM generation, and both attestations.
- Strict-TLS Chromium text and random 256 KiB file scenarios passed over forced
  `relay/relay` UDP TURN and natural direct `srflx/srflx` UDP. Checksums matched
  in all four scenarios.
- A native-native random 1 MiB transfer disabled direct TCP and LAN discovery,
  negotiated `service-native-relay` with temporary credentials, and used four
  striped connections through `106.53.170.243:5140`. Source and destination
  both had SHA-256
  `6f28453d027ce26e9b55acb10b6162099a80014e63e512d4a548ec41057f7846`.
- The acknowledged end-to-end rate was 225 KB/s at the sender and 231 KB/s at
  the receiver. Both services were active with zero restarts and no warning
  journal entries. Health reported no active TURN allocations, dropped bytes,
  or quota failures.

The `v0.1.0-alpha.3` deployment passed these checks on 2026-07-29:

- The release workflow passed Windows same-port direct tests, source tests and
  vet, five-platform builds, archive verification, CycloneDX SBOM generation,
  build-provenance attestation, and SBOM attestation.
- The no-version installer selected `v0.1.0-alpha.3`, verified its checksum,
  and installed commit `515db4c7f848bf04300c3fddf15dd7d23f3ccd3e`.
- A native-native random 1 MiB transfer disabled direct TCP and LAN discovery,
  negotiated `service-native-relay` with temporary credentials, and used four
  striped connections through `106.53.170.243:5140`. Source and destination
  both had SHA-256
  `da89889c6299e8b70286995805159f4be2ec3f674ba22bac483feaf6ed2cbc62`.
- The acknowledged end-to-end rate was 271 KB/s at the sender and 281 KB/s at
  the receiver. Per-path socket measurements were separately labeled as
  transport-write rates.
- Both services were active with zero restarts. Ports `1001/tcp`, `5140/tcp`,
  and `5140/udp` were listening, and `/api/health` reported the release version
  with no active TURN allocations, dropped bytes, or quota failures.

The `health1` deployment passed these checks on 2026-07-29:

- Pull request 15 passed all eight required and supporting jobs, including Go
  race tests, Linux and Windows smoke tests, Chromium, Firefox, WebKit,
  container validation, and release artifact verification.
- Strict-TLS Chromium text and random 256 KiB file scenarios passed over both
  forced `relay/relay` UDP TURN and natural direct `srflx/srflx` UDP. File
  checksums matched in both runs.
- A native-native random 1 MiB transfer disabled direct TCP and LAN discovery,
  negotiated the service relay with temporary credentials, used four striped
  connections through `106.53.170.243:5140`, and matched SHA-256
  `ba486b2d5f4edc8144297eb7ccdec57f21c7bf5009d4ce6b449d5c03f36c9282`.
- A native Notepad wrote revision 1, then reopened with an equal encrypted local
  draft. The server snapshot remained at generation 1 with an unchanged expiry.
  After restarting `kigo-public.service`, a fresh client recovered the same
  revision and plaintext.
- Both services remained active with zero restarts and no warning-level journal
  entries. Ports `1001/tcp`, `5140/tcp`, and `5140/udp` were listening, the
  certificate renewal timer was active, and post-test health reported no active
  TURN allocations.

The `v0.1.0-alpha.2` persistent-notepad deployment passed these public checks on
2026-07-28:

- The no-version installer selected `v0.1.0-alpha.2`, verified its release
  checksum, and installed a client whose default health probe used
  `https://106.53.170.243:1001/api/health`.
- The release workflow built and verified five platform archives, generated a
  CycloneDX SBOM, and published build-provenance and SBOM attestations.
- Trusted HTTPS returned `persistent-note-v1` and a configured 30-day Notepad
  TTL from `/api/health`; the public `note.js` contained the persistent client.
- A native client edited revision 1 while alone. After restarting
  `kigo-public.service`, a later native client recovered the same text.
- A fresh Chromium context edited while alone and reached `Synced revision 1`.
  A second context with no origin-local storage recovered the service snapshot
  and displayed `Available`.
- On-disk files had hashed names and mode `0600`; scanning them found no
  pairing code, pad name, or plaintext. Both service units and the certificate
  renewal timer remained active after deployment.

The `v0.1.0-alpha.1` deployment passed all three public transfer paths on
2026-07-24:

- A forced-TURN Chromium matrix transferred encrypted text and a random 256 KiB
  file with matching SHA-256 checksums. Both peers selected `relay/relay` UDP.
  The redacted local evidence is in
  `artifacts/v0.1.0-alpha.1-forced-turn/matrix.json`.
- A natural-ICE Chromium matrix transferred the same scenarios with matching
  checksums. Both peers selected direct `srflx/srflx` UDP, while authenticated
  TURN remained advertised as fallback. The redacted local evidence is in
  `artifacts/v0.1.0-alpha.1-natural/matrix.json`.
- A native-native 1 MiB random file transfer disabled direct TCP and LAN
  discovery. Signaling selected `service-native-relay`, issued a temporary
  room-bound credential, and used four striped TCP relay connections through
  `106.53.170.243:5140`. The source and received file both had SHA-256
  `ad8b2332d463198b289626f6f2b345986ab387fbdf46c77ab40a25ad767eea48`.

For the native service-relay test, leave transport policy at its default
`auto`. `--transport native` requires an explicit local `--relay`, while the
default policy can wait for signaling to advertise the service relay. Use
`--no-direct --no-lan` to make the negotiated service relay the only viable
native route.

After these transfers, both systemd units were active; `1001/tcp`, `5140/tcp`,
and `5140/udp` were listening. `/api/health` reported the release version, zero
active TURN allocations, zero dropped bytes, and zero quota failures.

On 2026-07-28, the self-signed certificate was replaced with a publicly trusted
Let's Encrypt IP certificate. Chromium text and random 256 KiB file scenarios
passed without ignoring TLS errors in both natural ICE and forced-TURN modes.
Natural ICE selected direct `srflx/srflx` UDP, while forced TURN selected
`relay/relay` UDP. The redacted local evidence is in
`artifacts/vps-ip-cert-20260728-natural/matrix.json` and
`artifacts/vps-ip-cert-20260728-forced-turn/matrix.json`. Post-test health again
reported zero active allocations, dropped bytes, and quota failures.

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

Let's Encrypt made short-lived IP address certificates generally available in
2026, so this deployment no longer needs a domain, `sslip.io`, or `nip.io`.
Certbot 5.7.0 is installed in `/opt/certbot` and requests the mandatory
`shortlived` profile with `--ip-address 106.53.170.243`. Certificates are valid
for about 160 hours.

HTTP-01 uses `/opt/speidio/dist` as its webroot because the existing port 80
server already serves files below `.well-known/acme-challenge`. The Kigo service
continues to terminate trusted TLS directly on port 1001. A systemd timer checks
renewal twice daily with up to one hour of random delay. Certbot's deploy hook
copies the renewed full chain and private key into `/etc/kigo/tls` with the
existing ownership and restarts only `kigo-public.service`.

The initial issuance used:

```sh
sudo apt-get install --yes python3-venv
sudo python3 -m venv /opt/certbot
sudo /opt/certbot/bin/pip install certbot==5.7.0
sudo /opt/certbot/bin/certbot certonly \
  --preferred-profile shortlived \
  --webroot --webroot-path /opt/speidio/dist \
  --ip-address 106.53.170.243 \
  --agree-tos --register-unsafely-without-email --non-interactive
```

Install `deploy/certbot-kigo-deploy-hook.sh` under
`/etc/letsencrypt/renewal-hooks/deploy/kigo-public`, and install the two
`deploy/kigo-certbot-renew.*` units under `/etc/systemd/system`. Then run
`systemctl daemon-reload` and enable `kigo-certbot-renew.timer`.

Validate the complete renewal path with:

```sh
ssh kiko_vps 'sudo /opt/certbot/bin/certbot renew --dry-run --no-random-sleep-on-renew'
curl --fail https://106.53.170.243:1001/api/health
```

The previous self-signed certificate and key are retained as
`/etc/kigo/tls/server.crt.self-signed-20260728` and
`/etc/kigo/tls/server.key.self-signed-20260728`. They are an emergency service
rollback only; browsers will show a certificate warning after rollback.
