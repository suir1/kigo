# Shared Notepad

`kigo note` is an asynchronous encrypted shared document. It does not wait for a second client and does not use
the file-transfer TCP, relay, or WebRTC route. The first client can create or open a pad, edit immediately, leave,
and recover the latest shared contents later. Other native or browser clients can open the same code and pad at
any time while the snapshot remains available.

```sh
kigo note host
kigo note join K7M9Q2
```

`host` creates a random six-character code by default, or opens the existing document if that code and pad were
already used. It prints a native command and a browser link. The default pad uses
`https://kigo.example/#n=K7M9Q2`; a custom pad uses a link such as
`https://kigo.example/#n=K7M9Q2&p=Sprint+Notes`.

The public browser, native CLI, loopback `kigo web` console, and `kigo tui` all connect to the same persistent
notepad service. Browser links keep the code and pad in the URL fragment, so browsers do not send either value in
the initial HTTP request. The client derives hashed room and pad identifiers and opens:

```text
WS /api/note-sync/{room_token}/{pad_token}
```

Once the WebSocket is available, the editor is enabled regardless of whether another client is online. Updates
are broadcast to all connected clients. A client that opens later receives the latest encrypted snapshot. An
interrupted client retries up to three total connection attempts by default; native reconnect behavior uses
`--no-reconnect`, `--reconnect-attempts`, and `--reconnect-delay`.

## User Interfaces

Hosts can choose a 6-64 character alphanumeric or hyphen-separated mnemonic code:

```sh
kigo note host --code project-alpha-2026
kigo note --pad scratch host
kigo note --pad scratch join K7M9Q2
```

The default pad is `main`. Missing or empty pad values normalize to `main`, so existing `#n=<code>` links remain
valid. One client edits one selected pad at a time; leave before switching pads.

Inside the native CLI session:

- Enter a line to publish the complete contents of the selected pad.
- `/show` prints the local document.
- `/clear` publishes an empty document.
- `/quit` closes only the current client.

The public and loopback browser editors publish after a 250 ms debounce. Clear publishes an empty revision and
Leave disconnects only that browser. The TUI uses the same debounce and supports `Ctrl+S` to publish immediately,
`Ctrl+L` to clear, and Esc to leave. Its Recent row uses Left/Right to select an entry, `f` to toggle its favorite
state, and `x` to remove it from the local list.

## Recent Notepads

A client records a notepad only after it has connected, decrypted the persistent record, and completed workspace
synchronization. A bad code or failed connection therefore does not pollute the list. Each catalog retains at most
20 entries, sorts favorites first and then by last-opened time, and contains only the normalized code, pad,
favorite flag, and timestamp. It never contains note text.

The native CLI, loopback web console, and TUI share `note-recents.json` next to `config.json`. It is written
atomically with mode `0600`. Set `KIGO_NOTE_RECENTS_PATH` to use another location. Native catalog commands are:

```sh
kigo note recent
kigo note recent --json
kigo note --pad scratch favorite K7M9Q2
kigo note --pad scratch unfavorite K7M9Q2
kigo note --pad scratch forget K7M9Q2
```

The public web app keeps a separate catalog in origin-local `localStorage` under `kigo-note-recent-v1`. Browser
catalogs do not cross origins, so an IP-based deployment and a later domain have different lists. Clearing browser
site data removes this list and encrypted browser drafts but does not remove the service snapshot.

Codes and pad names are intentionally plaintext in these local catalogs so an entry can reopen with one action.
Anyone who can read the native account files or browser profile can see that metadata and reuse a still-valid
code. Remove sensitive entries after use or avoid the recent-list feature by clearing the catalog.

## Persistence And Conflicts

Each update contains a pad, revision, timestamp, and complete text. The service assigns a monotonically increasing
generation and accepts an update only when its base generation is current. A stale writer receives the newest
snapshot, and clients deterministically reconcile documents by revision, timestamp, then text. This is a compact
last-writer convergence model, not a character-level collaborative editor or CRDT.

Snapshots expire after 30 days without an update by default. Every accepted update refreshes the deadline. Set the
service options directly or through the matching environment variables:

```sh
kigo serve --note-store /var/lib/kigo/notes --note-ttl 720h

KIGO_NOTE_STORE=/var/lib/kigo/notes
KIGO_NOTE_TTL=720h
```

An empty `--note-store` keeps snapshots in server memory and therefore does not survive a restart. Production
deployments should use a durable directory writable only by the Kigo service account. The service cleanup loop
removes expired in-memory and on-disk snapshots. `/api/health` reports whether disk persistence is configured,
the loaded document and client counts, and the configured TTL under `notepad`.

The current protocol permits up to 16 simultaneous clients per document and limits one document to 1 MiB. It
synchronizes one pad per connection and is separate from file manifests, transfer resume, and mux.

## Encryption Model

Clients serialize the document and encrypt it with AES-128-GCM. The key is derived with
`HKDF-SHA256(normalized_code, random_salt, "kigo-note-store-v1")`; every snapshot uses a fresh 16-byte salt and
12-byte nonce. The normalized pad is authenticated as additional data. Go and WebCrypto use the same record
format.

The service stores only record metadata, random salt, nonce, and ciphertext. Disk filenames are hashes of the
hashed room/pad identity, and tests verify that the code, pad, and text do not appear in snapshot files. The
service can still observe connection metadata, update times, ciphertext size, and stable hashed identifiers.
Because a six-character code can be guessed offline from a stolen record, this protects routine server-side
content exposure but is not a high-security password model. Use a longer custom code for more sensitive notes.

## Local Draft Cache

Native CLI, loopback web, TUI, and public browser clients also keep an encrypted local draft for fast recovery
when a publish has not reached the service. Drafts expire after seven days and are scoped to the normalized code,
role (`host` or `join`), and pad. Draft keys use
`HKDF-SHA256(code, random_salt, "kigo-note-draft-v1")`; AES-128-GCM uses a new nonce for every save and
authenticates the role and pad.

Native drafts are `0600` atomic files in the platform config directory's `kigo/note-drafts/` folder. Set
`KIGO_NOTE_DRAFT_PATH` to override it or pass `--no-note-drafts` to disable the cache. Native retains at most 16
drafts; browsers retain up to three entries in origin-local storage as quota permits. Deleting local drafts does
not delete the service snapshot: opening the same code and pad downloads it again until its service TTL expires.
