# VPS public relays

Kigo's public web/signaling service, native TCP relay, and WebRTC TURN relay run
on the `kiko_vps` SSH host. This file is the current operations guide. Historical
deployment-by-deployment notes are archived in
`docs/archive/vps-deployment-history.md`.

## Endpoints

- Web app and signaling: `https://106.53.170.243:1001` (`1001/tcp`)
- Native TCP relay: `106.53.170.243:5140` (`5140/tcp`)
- Built-in TURN: `turn:106.53.170.243:5140` (`5140/udp`)
- TURN allocation range: `49160-49259/udp`
- systemd units: `kigo-public.service` and `kigo-relay.service`

TCP and UDP use separate port spaces, so both relay protocols use port 5140.
The signaling service advertises the native relay and returns room-bound TURN
credentials from `/api/ice`. Clients normally configure only the service URL.

## Client setup

```sh
kigo config set service https://106.53.170.243:1001
kigo config unset tls-ca
kigo route --pair native-web --json
```

The service uses a publicly trusted Let's Encrypt IP certificate. The
certificate is short-lived and renewed automatically; do not pin a documented
fingerprint.

## Current deployment

Verified on 2026-09-28:

- version: `v0.1.0-dev.20260928.opfs-retry1`
- source commit: `0fb82bab4f2f8bb7416a2aade2fc91ddd71842b5`
- build date: `2026-09-28T23:06:58Z`
- target: Go 1.26.6, Linux amd64
- `/usr/local/bin/kigo` SHA-256:
  `26224cc640f798cee41c0df31eb83e7de92c7a61ed98d265a301d3492972301d`
- rollback binary:
  `/usr/local/bin/kigo.backup-20260928-230658-opfs-recovery2`
- `kigo-public.service`: active
- `kigo-relay.service`: active
- pull request 73 passed Go/protocol, Chromium, Firefox, WebKit, Linux,
  Windows, container, and release-layout checks
- local Go/Python/relay/native-web suites passed, including a stale first OPFS
  snapshot with memory recovery disabled and persistent-corruption recovery
- public Chromium file transfer passed over direct UDP WebRTC (`srflx/srflx`)
  with strict TLS verification and matching SHA-256 for 167,772,177 bytes
- the large-file receiver reported OPFS `sync-worker` storage with no memory
  recovery; payload throughput was 2.175 MiB/s, OPFS writes took 1.952s, and
  the peak persistent-write queue was 262,144 bytes

Both services load `/usr/local/bin/kigo`. The public service stores encrypted
shared-note snapshots under `/var/lib/kigo/notes`. Secrets remain in root/group
restricted environment files and must not be copied into test artifacts or
source control.

## Operations

```sh
ssh kiko_vps 'systemctl status kigo-public.service kigo-relay.service'
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
- `/etc/letsencrypt/live/106.53.170.243/`
- `/etc/letsencrypt/renewal-hooks/deploy/kigo-public`
- `/etc/systemd/system/kigo-certbot-renew.service`
- `/etc/systemd/system/kigo-certbot-renew.timer`

## Verification

```sh
ssh kiko_vps '/usr/local/bin/kigo version --json'
ssh kiko_vps 'sha256sum /usr/local/bin/kigo'
curl -fsS https://106.53.170.243:1001/api/health
curl -fsS https://106.53.170.243:1001/api/ice
```

Run public transfer matrices from the repository when changing signaling, ICE,
TURN, relay, chunking, resume, or integrity behavior:

```sh
KIGO_PUBLIC_BROWSER_URL=https://106.53.170.243:1001 \
KIGO_ARTIFACT_DIR="$PWD/artifacts/public-browser" \
./scripts/smoke_public_browser.sh

KIGO_ARTIFACT_DIR="$PWD/artifacts/public-matrix" \
./scripts/public_matrix.sh
```

Generated artifacts are ignored by Git. Retain only results needed for an
active investigation and delete old local matrices after recording the useful
conclusion in a regression test or issue.

## Certificate renewal

The certificate renewal timer must remain enabled. The deploy hook copies the
renewed full chain and private key into `/etc/kigo/tls` with the existing
ownership and restarts only `kigo-public.service`. Verify the live certificate
instead of relying on a stored fingerprint:

```sh
ssh kiko_vps 'openssl x509 -in /etc/kigo/tls/server.crt -noout -dates -fingerprint -sha256'
```
