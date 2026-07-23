# Shared Notepad

Native and browser notepad sessions use the same five-minute pairing window as file and text transfers. Native
clients can change it with `--pair-timeout`; the timeout is removed once the encrypted notepad session connects.
After a network interruption, current native and browser clients retry the same code up to three total attempts.
Native clients use `--no-reconnect`, `--reconnect-attempts`, and `--reconnect-delay` to control this behavior.

`kigo note` is a live, bidirectional notepad for short text with an encrypted local draft cache. It uses
the same route selection as native file transfer:

```sh
kigo note host
kigo note join K7M9Q2
```

The host prints a six-character random pairing code by default, a native join command, and a browser link.
The default pad uses `https://kigo.example/#n=K7M9Q2`; a custom pad uses a link such as
`https://kigo.example/#n=K7M9Q2&p=Sprint+Notes`. The pad remains in the URL fragment and is not sent to the
signaling service. The other peer can use either the native client or the web app's Notepad tab. Native/native sessions use the selected
direct, relay, or WebRTC route. Any session containing a browser uses WebRTC.
The transport is bidirectional even though rendezvous roles are named
`sender` and `receiver`.

The loopback `kigo web` console also includes a Notepad tab. It hosts or joins
one selected pad through the same native route selection and keeps the
session in the local process. Its `/api/note/*` endpoints require the random
console token. Text is sent peer-to-peer; the console stores only its own encrypted local draft and never uploads
it to the signaling service.

`kigo tui` exposes the same host/join and pad selection flow with a multiline editor. It publishes after a
250 ms debounce; `Ctrl+S` publishes
immediately, `Ctrl+L` clears the shared document, and Esc leaves the session.

Hosts can choose a 6-64 character alphanumeric or hyphen-separated mnemonic code with
`kigo note host --code project-alpha-2026`. Browser, local web, and TUI hosts expose the same optional field.

Inside the session:

- Enter a line to publish the complete contents of the selected pad.
- `/show` prints the local document.
- `/clear` publishes an empty document.
- `/quit` closes the session.

In the browser:

- Host creates a code and a `#n=<code>` share link; custom pads add `&p=<pad>`.
- Join accepts a generated or custom code and pad; opening either link form joins automatically.
- Editor changes publish after a 250 ms debounce.
- Clear publishes an empty document; Leave sends a final `bye`.

The default pad is `main`. Use `--pad` to select another single pad name:

```sh
kigo note --pad scratch host
kigo note --pad scratch join K7M9Q2
```

Native CLI, public browser, loopback web, and TUI sessions all select one pad when the session starts. Missing
or empty pad values normalize to `main`, so old `#n=<code>` links remain compatible. The selected pad is locked
while connected; leave the session before switching to another pad.

The notepad protocol is separate from the file manifest protocol. It performs
a small plaintext hello exchange only to exchange fresh nonces, then derives
independent host-to-join and join-to-host AES-128-GCM sessions with
`HKDF-SHA256(code, sender_nonce || receiver_nonce, "kigo-note-v1:...")`.
Encrypted frames have independent sequence counters in each direction; replay,
out-of-order, unsupported-version, oversized-text, and authentication failures
are rejected.

Current clients advertise the optional `workspace_sync` hello capability. After reconnecting, they exchange the
selected pad in a fixed host-then-join order over the new encrypted session and deterministically keep the newer
revision. The fixed order also prevents both native peers from blocking while writing large snapshots. Older
clients ignore the capability and keep the original one-shot session behavior, so the protocol version remains 1.

Each update carries a pad, revision,
timestamp, and full text. Concurrent updates converge using deterministic
revision, timestamp, and text ordering. Negotiation and signaling tag rooms as
`note`, so regular file/text Receive clients are rejected before the encrypted
handshake. The current MVP limits one document to 1 MiB and does not synchronize multiple pads in one session.

Native CLI, loopback web, TUI, and public browser clients keep encrypted drafts for seven days. A draft is scoped
to the normalized pairing code, role (`host` or `join`), and pad; all three must match to restore it. Draft keys use
`HKDF-SHA256(code, random_salt, "kigo-note-draft-v1")`, AES-128-GCM uses a fresh random nonce on every save, and
the role and pad are authenticated as additional data. The storage index is a hash derived from the room token,
role, and pad, so filenames/localStorage keys do not expose them.

Native drafts are separate `0600` atomic files under the platform config directory's `kigo/note-drafts/` folder.
Set `KIGO_NOTE_DRAFT_PATH` to override that directory or pass `--no-note-drafts` to disable native persistence.
The native cache retains at most 16 drafts. Browser drafts use origin-local storage and retain up to three entries
as quota permits. Clear saves an empty revision; deleting the native draft directory or the browser site's local
data removes the cache immediately. A random host code still needs to be retained by the user before that draft
can be addressed again.
