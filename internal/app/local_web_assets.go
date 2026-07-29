package app

const localWebHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Kigo Local</title>
  <link rel="stylesheet" href="/local.css">
</head>
<body>
  <header class="topbar">
    <div class="brand">
      <div class="brand-mark" aria-hidden="true">K</div>
      <div>
        <h1>Kigo Local</h1>
        <p>Native transfer console</p>
      </div>
    </div>
    <div id="routeBadge" class="route-badge">Loading route</div>
  </header>

  <main>
    <nav class="tabs" aria-label="Tools">
      <button class="tab active" data-tab="send" type="button">Send</button>
      <button class="tab" data-tab="recv" type="button">Receive</button>
      <button class="tab" data-tab="note" type="button">Notepad</button>
      <button class="tab" data-tab="doctor" type="button">Doctor</button>
    </nav>

    <section class="workspace">
      <form id="sendPanel" class="tool-panel active" data-panel="send">
        <div class="panel-heading">
          <div>
            <h2>Send from this computer</h2>
            <p>The peer can use Kigo or open the generated public link.</p>
          </div>
        </div>
        <label for="sendPath">File or directory path</label>
        <div class="path-control">
          <input id="sendPath" name="path" autocomplete="off" placeholder="/Users/me/Desktop/archive.zip" required>
          <button id="browseSendPath" class="secondary-button" type="button">Browse</button>
        </div>
        <div class="form-grid">
          <div>
            <label for="sendCode">Pairing code (optional)</label>
            <input id="sendCode" maxlength="80" autocomplete="off" placeholder="Random if blank">
          </div>
          <div>
            <label for="symlinks">Symbolic links</label>
            <select id="symlinks">
              <option value="follow">Follow targets</option>
              <option value="preserve">Preserve safe links</option>
            </select>
          </div>
          <label class="check-row">
            <input id="noGitIgnore" type="checkbox">
            <span>Include paths ignored by .gitignore</span>
          </label>
        </div>
        <button class="primary" type="submit">Start sending</button>
      </form>

      <form id="recvPanel" class="tool-panel" data-panel="recv">
        <div class="panel-heading">
          <div>
            <h2>Receive on this computer</h2>
            <p>Files use native resume and conflict handling.</p>
          </div>
        </div>
        <div class="form-grid">
          <div>
            <label for="recvCode">Pairing code</label>
              <input id="recvCode" maxlength="80" autocomplete="off" placeholder="K7M9Q2" required>
          </div>
          <div>
            <label for="outputDir">Output directory</label>
            <div class="path-control">
              <input id="outputDir" autocomplete="off" value=".">
              <button id="browseOutputDir" class="secondary-button" type="button">Browse</button>
            </div>
          </div>
          <div>
            <label for="conflict">Existing files</label>
            <select id="conflict">
              <option value="overwrite">Overwrite</option>
              <option value="rename">Rename incoming</option>
              <option value="skip">Skip</option>
            </select>
          </div>
        </div>
        <button class="primary" type="submit">Start receiving</button>
      </form>

      <section id="notePanel" class="tool-panel" data-panel="note">
        <div class="panel-heading">
          <div>
            <h2>Shared notepad</h2>
            <p id="notePadSummary">Pad: main</p>
          </div>
        </div>
        <div class="note-connect">
          <button id="hostNote" class="primary compact" type="button">Create notepad</button>
          <div class="note-join">
            <div class="note-fields">
              <div>
                <label for="noteCode">Pairing code</label>
                <input id="noteCode" maxlength="80" autocomplete="off" placeholder="Random for create">
              </div>
              <div>
                <label for="notePad">Pad</label>
                <input id="notePad" maxlength="64" autocomplete="off" value="main">
              </div>
            </div>
            <button id="joinNote" class="secondary-button" type="button">Open</button>
          </div>
        </div>
        <div class="note-recents-section">
          <div class="note-recents-heading">
            <strong>Recent notepads</strong>
            <span>Stored on this device</span>
          </div>
          <div id="noteRecents" class="note-recents"><span class="note-recents-empty">No recent notepads.</span></div>
        </div>
        <div id="noteShareArea" class="note-share hidden">
          <div>
            <span class="eyebrow">Pairing code</span>
            <strong id="notePairingCode"></strong>
          </div>
          <div class="share-link-row">
            <input id="noteShareLink" readonly aria-label="Public notepad link">
            <button id="copyNoteLink" type="button">Copy link</button>
          </div>
        </div>
        <label for="noteEditor">Shared text</label>
        <textarea id="noteEditor" rows="12" disabled></textarea>
        <div class="note-toolbar">
          <span id="noteStatus" class="note-status">Not connected</span>
          <div>
            <button id="clearNote" type="button" disabled>Clear</button>
            <button id="leaveNote" type="button" disabled>Leave</button>
          </div>
        </div>
      </section>

      <form id="doctorPanel" class="tool-panel" data-panel="doctor">
        <div class="panel-heading">
          <div>
            <h2>Check transfer routes</h2>
            <p>Tests signaling, relay, direct listening, and route history.</p>
          </div>
        </div>
        <label for="doctorTimeout">Check timeout</label>
        <select id="doctorTimeout">
          <option value="2s">2 seconds</option>
          <option value="3s" selected>3 seconds</option>
          <option value="5s">5 seconds</option>
          <option value="10s">10 seconds</option>
        </select>
        <button class="primary" type="submit">Run doctor</button>
      </form>
    </section>

    <section class="task-section" aria-live="polite">
      <div class="task-heading">
        <div>
          <span class="eyebrow">Current task</span>
          <h2 id="taskTitle">Idle</h2>
        </div>
        <button id="cancelButton" class="icon-button hidden" type="button" title="Cancel current task" aria-label="Cancel current task">×</button>
      </div>

      <div id="shareArea" class="share-area hidden">
        <div>
          <span class="eyebrow">Pairing code</span>
          <strong id="pairingCode"></strong>
        </div>
        <div class="share-link-row">
          <input id="shareLink" readonly aria-label="Public share link">
          <button id="copyLink" type="button">Copy link</button>
        </div>
      </div>

      <div id="taskMessage" class="task-message">Choose an action above.</div>
      <pre id="jobLog" class="job-log">No task output yet.</pre>
    </section>
  </main>

  <dialog id="pathBrowserDialog" class="path-browser-dialog" aria-labelledby="pathBrowserTitle">
    <div class="path-browser-shell">
      <header class="path-browser-header">
        <h2 id="pathBrowserTitle">Choose path</h2>
        <button id="closePathBrowser" class="icon-button" type="button" title="Close path browser" aria-label="Close path browser">×</button>
      </header>
      <div class="path-browser-location">
        <button id="pathBrowserUp" class="secondary-button" type="button">Up</button>
        <input id="pathBrowserCurrent" autocomplete="off" aria-label="Current directory">
        <button id="pathBrowserGo" class="secondary-button" type="button">Go</button>
      </div>
      <div class="path-browser-tools">
        <input id="pathBrowserFilter" autocomplete="off" placeholder="Filter names" aria-label="Filter names">
        <select id="pathBrowserSort" aria-label="Path sort order">
          <option value="name">Name</option>
          <option value="modified">Modified</option>
        </select>
      </div>
      <div id="pathBrowserError" class="path-browser-error hidden"></div>
      <div class="path-browser-list">
        <table class="path-browser-table">
          <thead>
            <tr><th>Name</th><th>Modified</th><th>Type</th><th></th></tr>
          </thead>
          <tbody id="pathBrowserEntries"></tbody>
        </table>
      </div>
      <footer class="path-browser-footer">
        <span id="pathBrowserCount"></span>
        <button id="selectCurrentPath" class="primary" type="button">Choose current folder</button>
      </footer>
    </div>
  </dialog>

  <script src="/local.js"></script>
</body>
</html>
`

const localWebCSS = `:root {
  color-scheme: light;
  --ink: #17212b;
  --muted: #607080;
  --line: #d9e0e6;
  --surface: #ffffff;
  --surface-alt: #f4f7f8;
  --green: #117a5b;
  --green-dark: #0b5b43;
  --blue: #285f9e;
  --amber: #a96608;
  --red: #b42318;
  --shadow: 0 10px 28px rgba(23, 33, 43, 0.08);
  font-family: Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
}

* { box-sizing: border-box; }

body {
  margin: 0;
  min-height: 100vh;
  background: var(--surface-alt);
  color: var(--ink);
}

button, input, select, textarea { font: inherit; }

button { cursor: pointer; }

.topbar {
  min-height: 78px;
  padding: 14px clamp(18px, 4vw, 48px);
  background: var(--ink);
  color: #fff;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 24px;
}

.brand {
  display: flex;
  align-items: center;
  gap: 12px;
}

.brand-mark {
  width: 42px;
  height: 42px;
  display: grid;
  place-items: center;
  background: #d9f35b;
  color: var(--ink);
  border-radius: 6px;
  font-size: 22px;
  font-weight: 800;
}

h1, h2, p { margin: 0; }

h1 {
  font-size: 20px;
  line-height: 1.2;
  letter-spacing: 0;
}

.brand p {
  margin-top: 3px;
  color: #b9c5cf;
  font-size: 13px;
}

.route-badge {
  max-width: min(50vw, 560px);
  color: #dbe5ec;
  font-size: 13px;
  text-align: right;
  overflow-wrap: anywhere;
}

main {
  width: min(1040px, 100%);
  margin: 0 auto;
  background: var(--surface);
  min-height: calc(100vh - 78px);
  box-shadow: var(--shadow);
}

.tabs {
  height: 52px;
  display: flex;
  align-items: stretch;
  border-bottom: 1px solid var(--line);
  padding: 0 clamp(18px, 4vw, 42px);
}

.tab {
  min-width: 108px;
  border: 0;
  border-bottom: 3px solid transparent;
  background: transparent;
  color: var(--muted);
  font-weight: 650;
}

.tab:hover { color: var(--ink); }
.tab.active { color: var(--green-dark); border-bottom-color: var(--green); }

.workspace {
  padding: 30px clamp(18px, 4vw, 42px) 34px;
  border-bottom: 1px solid var(--line);
}

.tool-panel { display: none; }
.tool-panel.active { display: block; }

.panel-heading {
  display: flex;
  justify-content: space-between;
  gap: 24px;
  margin-bottom: 24px;
}

.panel-heading h2, .task-heading h2 {
  font-size: 18px;
  line-height: 1.35;
  letter-spacing: 0;
}

.panel-heading p {
  margin-top: 5px;
  color: var(--muted);
  font-size: 14px;
}

label {
  display: block;
  margin-bottom: 7px;
  color: #344250;
  font-size: 13px;
  font-weight: 650;
}

input, select, textarea {
  width: 100%;
  border: 1px solid #bdc8d1;
  border-radius: 5px;
  background: #fff;
  color: var(--ink);
  padding: 9px 11px;
  outline: none;
}

input, select { min-height: 42px; }

textarea {
  min-height: 260px;
  resize: vertical;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  line-height: 1.5;
}

input:focus, select:focus, textarea:focus {
  border-color: var(--blue);
  box-shadow: 0 0 0 3px rgba(40, 95, 158, 0.12);
}

.path-control {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  gap: 8px;
}

.secondary-button {
  min-height: 42px;
  border: 1px solid #98a9b8;
  border-radius: 5px;
  background: #fff;
  color: var(--ink);
  padding: 8px 14px;
  font-weight: 650;
}

.secondary-button:hover { border-color: var(--blue); color: var(--blue); }
.secondary-button:disabled { cursor: not-allowed; opacity: 0.5; }

.form-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 18px;
  margin-top: 18px;
}

.check-row {
  min-height: 42px;
  margin: 22px 0 0;
  display: flex;
  align-items: center;
  gap: 9px;
  font-weight: 500;
}

.check-row input {
  width: 17px;
  min-height: 17px;
  height: 17px;
}

.primary {
  min-height: 42px;
  margin-top: 22px;
  border: 1px solid var(--green-dark);
  border-radius: 5px;
  background: var(--green);
  color: #fff;
  padding: 9px 18px;
  font-weight: 700;
}

.primary:hover { background: var(--green-dark); }
.primary:disabled { cursor: not-allowed; opacity: 0.5; }
.primary.compact { margin-top: 22px; white-space: nowrap; }

.note-connect {
  display: grid;
  grid-template-columns: auto minmax(280px, 1fr);
  align-items: end;
  gap: 18px;
  margin-bottom: 22px;
}

.note-join label { margin-bottom: 7px; }

.note-join {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  align-items: end;
  gap: 8px;
}

.note-fields {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(140px, 0.65fr);
  gap: 10px;
}

.note-recents-section {
  margin: 0 0 22px;
  border-top: 1px solid var(--line);
  border-bottom: 1px solid var(--line);
  padding: 14px 0;
}

.note-recents-heading,
.note-recent-row {
  display: flex;
  align-items: center;
  gap: 10px;
}

.note-recents-heading {
  justify-content: space-between;
  margin-bottom: 8px;
}

.note-recents-heading span,
.note-recents-empty,
.note-recent-pad {
  color: var(--muted);
  font-size: 13px;
}

.note-recent-row {
  min-height: 38px;
  border-top: 1px solid var(--line);
}

.note-recent-identity {
  min-width: 0;
  flex: 1;
  display: flex;
  gap: 8px;
  align-items: baseline;
}

.note-recent-code {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
}

.note-recent-row label {
  margin: 0;
  display: inline-flex;
  align-items: center;
  gap: 5px;
  font-size: 13px;
  white-space: nowrap;
}

.note-recent-row input { width: auto; }

.note-recent-row button {
  min-height: 30px;
  border: 1px solid #98a9b8;
  border-radius: 5px;
  background: #fff;
  color: var(--ink);
  padding: 4px 9px;
}

.inline-action {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  gap: 8px;
}

.inline-action button,
.note-toolbar button {
  min-height: 42px;
  border: 1px solid #98a9b8;
  border-radius: 5px;
  background: #fff;
  color: var(--ink);
  padding: 8px 14px;
  font-weight: 650;
}

.inline-action button:disabled,
.note-toolbar button:disabled {
  cursor: not-allowed;
  opacity: 0.5;
}

.note-share {
  margin: 0 0 22px;
  padding: 14px 0;
  border-top: 1px solid var(--line);
  display: grid;
  grid-template-columns: 180px 1fr;
  align-items: end;
  gap: 24px;
}

#notePairingCode {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 22px;
}

.note-toolbar {
  min-height: 48px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 18px;
}

.note-toolbar > div {
  display: flex;
  gap: 8px;
}

.note-status {
  color: var(--muted);
  font-size: 14px;
}

.note-status.running { color: var(--blue); }
.note-status.done { color: var(--green-dark); }
.note-status.failed { color: var(--red); }
.note-status.canceled { color: var(--amber); }

.task-section {
  padding: 28px clamp(18px, 4vw, 42px) 44px;
}

.task-heading {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 20px;
  margin-bottom: 18px;
}

.eyebrow {
  display: block;
  margin-bottom: 4px;
  color: var(--muted);
  font-size: 11px;
  font-weight: 750;
  text-transform: uppercase;
}

.icon-button {
  width: 38px;
  height: 38px;
  border: 1px solid #e3aaa4;
  border-radius: 5px;
  background: #fff;
  color: var(--red);
  font-size: 24px;
  line-height: 1;
}

.share-area {
  padding: 16px 0 18px;
  border-top: 1px solid var(--line);
  border-bottom: 1px solid var(--line);
  display: grid;
  grid-template-columns: 180px 1fr;
  align-items: end;
  gap: 24px;
}

#pairingCode {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 27px;
  letter-spacing: 0;
}

.share-link-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  gap: 8px;
}

.share-link-row button {
  border: 1px solid #98a9b8;
  border-radius: 5px;
  background: #fff;
  color: var(--ink);
  padding: 8px 14px;
  font-weight: 650;
}

.task-message {
  min-height: 24px;
  margin: 16px 0 10px;
  color: var(--muted);
  font-size: 14px;
}

.task-message.running { color: var(--blue); }
.task-message.failed { color: var(--red); }
.task-message.done { color: var(--green-dark); }
.task-message.canceled { color: var(--amber); }

.job-log {
  min-height: 210px;
  max-height: 390px;
  overflow: auto;
  margin: 0;
  padding: 16px;
  border: 1px solid #263540;
  border-radius: 5px;
  background: #111a21;
  color: #d9e7ef;
  font: 12px/1.55 ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.hidden { display: none !important; }

.path-browser-dialog {
  width: min(780px, calc(100vw - 32px));
  max-height: min(760px, calc(100vh - 32px));
  padding: 0;
  border: 1px solid #aebbc5;
  border-radius: 6px;
  background: var(--surface);
  color: var(--ink);
  box-shadow: 0 24px 60px rgba(23, 33, 43, 0.24);
}

.path-browser-dialog::backdrop { background: rgba(23, 33, 43, 0.52); }

.path-browser-shell {
  display: grid;
  grid-template-rows: auto auto auto auto minmax(180px, 1fr) auto;
  max-height: min(760px, calc(100vh - 32px));
}

.path-browser-header,
.path-browser-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 16px 18px;
}

.path-browser-header { border-bottom: 1px solid var(--line); }
.path-browser-header h2 { font-size: 17px; letter-spacing: 0; }
.path-browser-footer { border-top: 1px solid var(--line); color: var(--muted); font-size: 13px; }
.path-browser-footer .primary { margin-top: 0; }

.path-browser-location,
.path-browser-tools {
  display: grid;
  gap: 8px;
  padding: 12px 18px 0;
}

.path-browser-location { grid-template-columns: auto minmax(0, 1fr) auto; }
.path-browser-tools { grid-template-columns: minmax(0, 1fr) 160px; padding-bottom: 12px; }

.path-browser-error {
  margin: 0 18px 12px;
  color: var(--red);
  font-size: 13px;
}

.path-browser-list {
  min-height: 180px;
  overflow: auto;
  border-top: 1px solid var(--line);
}

.path-browser-table {
  width: 100%;
  border-collapse: collapse;
  table-layout: fixed;
}

.path-browser-table th,
.path-browser-table td {
  padding: 9px 12px;
  border-bottom: 1px solid #e7ecef;
  text-align: left;
  font-size: 13px;
  overflow-wrap: anywhere;
}

.path-browser-table th { color: var(--muted); font-size: 11px; text-transform: uppercase; }
.path-browser-table th:first-child { width: 42%; }
.path-browser-table th:nth-child(2) { width: 27%; }
.path-browser-table th:nth-child(3) { width: 13%; }
.path-browser-table th:last-child { width: 18%; }

.path-entry-name {
  border: 0;
  background: transparent;
  color: var(--blue);
  padding: 0;
  text-align: left;
  font-weight: 650;
  overflow-wrap: anywhere;
}

.path-entry-select {
  min-height: 34px;
  width: 100%;
  padding: 5px 9px;
}

.path-browser-empty { color: var(--muted); text-align: center !important; padding: 28px 12px !important; }

@media (max-width: 680px) {
  .topbar { align-items: flex-start; }
  .route-badge { max-width: 44vw; font-size: 12px; }
  .tabs { padding: 0 12px; }
  .tab { min-width: 0; flex: 1; }
  .workspace, .task-section { padding-left: 18px; padding-right: 18px; }
  .form-grid, .share-area, .note-connect, .note-share, .note-fields { grid-template-columns: 1fr; }
  .check-row { margin-top: 0; }
  .share-area { gap: 14px; }
  .primary.compact { width: 100%; margin-top: 0; }
  .note-toolbar { align-items: flex-start; flex-direction: column; }
  .path-browser-tools { grid-template-columns: 1fr; }
  .path-browser-table th:nth-child(2), .path-browser-table td:nth-child(2) { display: none; }
  .path-browser-table th:first-child { width: 50%; }
  .path-browser-table th:nth-child(3) { width: 20%; }
  .path-browser-table th:last-child { width: 30%; }
}
`

const localWebJS = `const token = new URLSearchParams(location.hash.slice(1)).get("token") || "";
const tabs = [...document.querySelectorAll("[data-tab]")];
const panels = [...document.querySelectorAll("[data-panel]")];
const transferForms = [...document.querySelectorAll("form.tool-panel")];
const taskTitle = document.querySelector("#taskTitle");
const taskMessage = document.querySelector("#taskMessage");
const jobLog = document.querySelector("#jobLog");
const cancelButton = document.querySelector("#cancelButton");
const shareArea = document.querySelector("#shareArea");
const pairingCode = document.querySelector("#pairingCode");
const shareLink = document.querySelector("#shareLink");
const noteCode = document.querySelector("#noteCode");
const notePad = document.querySelector("#notePad");
const notePadSummary = document.querySelector("#notePadSummary");
const noteEditor = document.querySelector("#noteEditor");
const noteStatus = document.querySelector("#noteStatus");
const noteShareArea = document.querySelector("#noteShareArea");
const notePairingCode = document.querySelector("#notePairingCode");
const noteShareLink = document.querySelector("#noteShareLink");
const noteRecents = document.querySelector("#noteRecents");
const hostNote = document.querySelector("#hostNote");
const joinNote = document.querySelector("#joinNote");
const clearNote = document.querySelector("#clearNote");
const leaveNote = document.querySelector("#leaveNote");
const pathBrowserDialog = document.querySelector("#pathBrowserDialog");
const pathBrowserTitle = document.querySelector("#pathBrowserTitle");
const pathBrowserCurrent = document.querySelector("#pathBrowserCurrent");
const pathBrowserUp = document.querySelector("#pathBrowserUp");
const pathBrowserFilter = document.querySelector("#pathBrowserFilter");
const pathBrowserSort = document.querySelector("#pathBrowserSort");
const pathBrowserEntries = document.querySelector("#pathBrowserEntries");
const pathBrowserList = document.querySelector(".path-browser-list");
const pathBrowserError = document.querySelector("#pathBrowserError");
const pathBrowserCount = document.querySelector("#pathBrowserCount");
let lastJobID = 0;
let lastLogCount = 0;
let lastNoteID = 0;
let currentJob = { running: false };
let currentNote = { running: false, connected: false, status: "idle", text: "" };
let noteDirty = false;
let noteEditVersion = 0;
let noteDebounce = null;
let noteRecentState = [];
let noteRecentLoadedID = 0;
let pathBrowserState = { target: null, mode: "send", response: null };

async function api(path, options = {}) {
  if (!token) throw new Error("Missing local console token. Reopen the URL printed by kigo web.");
  const response = await fetch(path, {
    ...options,
    headers: {
      "Content-Type": "application/json",
      "X-Kigo-Token": token,
      ...(options.headers || {}),
    },
  });
  const body = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(body.error || "Request failed");
  return body;
}

function browserModified(value) {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(date);
}

function chooseBrowserPath(path) {
  if (!pathBrowserState.target) return;
  pathBrowserState.target.value = path;
  pathBrowserDialog.close();
  pathBrowserState.target.focus();
}

function renderPathBrowser(response) {
  pathBrowserState.response = response;
  pathBrowserCurrent.value = response.current;
  pathBrowserUp.disabled = !response.parent;
  pathBrowserError.textContent = "";
  pathBrowserError.classList.add("hidden");

  const filter = pathBrowserFilter.value.trim().toLowerCase();
  const entries = (response.entries || []).filter((entry) => !filter || entry.name.toLowerCase().includes(filter));
  pathBrowserEntries.replaceChildren();
  pathBrowserList.scrollTop = 0;
  if (!entries.length) {
    const row = document.createElement("tr");
    const cell = document.createElement("td");
    cell.colSpan = 4;
    cell.className = "path-browser-empty";
    cell.textContent = filter ? "No matching paths" : "This folder is empty";
    row.appendChild(cell);
    pathBrowserEntries.appendChild(row);
  }
  entries.forEach((entry) => {
    const row = document.createElement("tr");
    const nameCell = document.createElement("td");
    const nameButton = document.createElement("button");
    nameButton.type = "button";
    nameButton.className = "path-entry-name";
    nameButton.textContent = entry.name + (entry.directory ? "/" : "");
    nameButton.addEventListener("click", () => {
      if (entry.directory) loadPathBrowser(entry.path);
      else chooseBrowserPath(entry.path);
    });
    nameCell.appendChild(nameButton);
    row.appendChild(nameCell);

    const modified = document.createElement("td");
    modified.textContent = browserModified(entry.modified);
    row.appendChild(modified);

    const type = document.createElement("td");
    type.textContent = entry.directory ? "Folder" : "File";
    row.appendChild(type);

    const actionCell = document.createElement("td");
    const action = document.createElement("button");
    action.type = "button";
    action.className = "secondary-button path-entry-select";
    action.textContent = "Choose";
    action.addEventListener("click", () => chooseBrowserPath(entry.path));
    actionCell.appendChild(action);
    row.appendChild(actionCell);
    pathBrowserEntries.appendChild(row);
  });
  pathBrowserCount.textContent = entries.length + (entries.length === 1 ? " item" : " items");
}

async function loadPathBrowser(path) {
  pathBrowserError.textContent = "";
  pathBrowserError.classList.add("hidden");
  pathBrowserCount.textContent = "Loading";
  try {
    const request = new URL("/api/browse", location.origin);
    request.searchParams.set("path", path || ".");
    request.searchParams.set("mode", pathBrowserState.mode);
    request.searchParams.set("sort", pathBrowserSort.value);
    renderPathBrowser(await api(request.pathname + request.search));
  } catch (error) {
    pathBrowserError.textContent = error.message;
    pathBrowserError.classList.remove("hidden");
    pathBrowserCount.textContent = "Unavailable";
  }
}

function openPathBrowser(target, mode, title) {
  pathBrowserState = { target, mode, response: null };
  pathBrowserTitle.textContent = title;
  pathBrowserFilter.value = "";
  pathBrowserSort.value = "name";
  pathBrowserDialog.showModal();
  loadPathBrowser(target.value);
}

function selectTab(name) {
  tabs.forEach((tab) => tab.classList.toggle("active", tab.dataset.tab === name));
  panels.forEach((panel) => panel.classList.toggle("active", panel.dataset.panel === name));
}

function syncControls() {
  const transferBlocked = Boolean(currentJob.running || currentNote.running);
  transferForms.forEach((form) => {
    [...form.elements].forEach((element) => { element.disabled = transferBlocked; });
  });
  const noteStartBlocked = Boolean(currentJob.running || currentNote.running);
  hostNote.disabled = noteStartBlocked;
  joinNote.disabled = noteStartBlocked;
  noteCode.disabled = noteStartBlocked;
  notePad.disabled = noteStartBlocked;
  noteEditor.disabled = !currentNote.connected;
  clearNote.disabled = !currentNote.connected;
  leaveNote.disabled = !currentNote.running;
  noteRecents.querySelectorAll('[data-note-recent-open]').forEach((button) => {
    button.disabled = noteStartBlocked;
  });
}

function renderNoteRecents(entries) {
  noteRecentState = Array.isArray(entries) ? entries : [];
  noteRecents.replaceChildren();
  if (!noteRecentState.length) {
    const empty = document.createElement("span");
    empty.className = "note-recents-empty";
    empty.textContent = "No recent notepads.";
    noteRecents.append(empty);
    return;
  }
  noteRecentState.forEach((entry) => {
    const row = document.createElement("div");
    row.className = "note-recent-row";
    const identity = document.createElement("div");
    identity.className = "note-recent-identity";
    const code = document.createElement("strong");
    code.className = "note-recent-code";
    code.textContent = entry.code;
    const pad = document.createElement("span");
    pad.className = "note-recent-pad";
    pad.textContent = entry.pad;
    identity.append(code, pad);
    const favoriteLabel = document.createElement("label");
    const favorite = document.createElement("input");
    favorite.type = "checkbox";
    favorite.checked = Boolean(entry.favorite);
    favorite.addEventListener("change", async () => {
      try {
        await api("/api/note/recents/favorite", {
          method: "POST",
          body: JSON.stringify({ code: entry.code, pad: entry.pad, favorite: favorite.checked }),
        });
        await refreshNoteRecents();
      } catch (error) {
        favorite.checked = !favorite.checked;
        noteStatus.textContent = "Failed: " + error.message;
        noteStatus.className = "note-status failed";
      }
    });
    favoriteLabel.append(favorite, document.createTextNode("Favorite"));
    const open = document.createElement("button");
    open.type = "button";
    open.dataset.noteRecentOpen = "";
    open.textContent = "Open";
    open.disabled = Boolean(currentJob.running || currentNote.running);
    open.addEventListener("click", () => {
      noteCode.value = entry.code;
      notePad.value = entry.pad;
      startNote("/api/note/join", { code: entry.code, pad: entry.pad });
    });
    const remove = document.createElement("button");
    remove.type = "button";
    remove.textContent = "Remove";
    remove.addEventListener("click", async () => {
      try {
        await api("/api/note/recents/forget", {
          method: "POST",
          body: JSON.stringify({ code: entry.code, pad: entry.pad }),
        });
        await refreshNoteRecents();
      } catch (error) {
        noteStatus.textContent = "Failed: " + error.message;
        noteStatus.className = "note-status failed";
      }
    });
    row.append(identity, favoriteLabel, open, remove);
    noteRecents.append(row);
  });
}

async function refreshNoteRecents() {
  try {
    renderNoteRecents(await api("/api/note/recents"));
  } catch (error) {
    noteRecents.textContent = "Recent notepads unavailable: " + error.message;
  }
}

function renderJob(job) {
  const changed = job.id !== lastJobID || (job.logs || []).length !== lastLogCount;
  lastJobID = job.id;
  lastLogCount = (job.logs || []).length;
  currentJob = job;
  syncControls();
  cancelButton.classList.toggle("hidden", !job.running);

  if (!job.id) {
    taskTitle.textContent = "Idle";
    taskMessage.textContent = "Choose an action above.";
    taskMessage.className = "task-message";
    jobLog.textContent = "No task output yet.";
    shareArea.classList.add("hidden");
    return;
  }

  const names = { send: "Sending", recv: "Receiving", doctor: "Network doctor" };
  taskTitle.textContent = names[job.kind] || "Task";
  taskMessage.className = "task-message";
  if (job.running) {
    taskMessage.textContent = job.code ? "Peer code is ready. Waiting or transferring." : "Task is running.";
    taskMessage.classList.add("running");
  } else if (job.canceled) {
    taskMessage.textContent = "Task canceled.";
    taskMessage.classList.add("canceled");
  } else if (job.failed) {
    taskMessage.textContent = "Failed: " + (job.error || "unknown error");
    taskMessage.classList.add("failed");
  } else {
    taskMessage.textContent = "Task completed.";
    taskMessage.classList.add("done");
  }

  const showShare = Boolean(job.code);
  shareArea.classList.toggle("hidden", !showShare);
  if (showShare) {
    pairingCode.textContent = job.code;
    shareLink.value = job.link || "";
  }
  jobLog.textContent = (job.logs || []).join("\n") || "Waiting for output...";
  if (changed) jobLog.scrollTop = jobLog.scrollHeight;
}

function noteStatusText(state) {
  let status = "Not connected";
  if (state.status === "opening") status = "Opening";
  else if (state.status === "available") status = "Available";
  else if (state.status === "waiting") status = "Waiting for peer";
  else if (state.status === "connecting") status = "Connecting";
  else if (state.status === "reconnecting") status = "Reconnecting";
  else if (state.status === "connected") status = "Connected";
  else if (state.status === "syncing") status = "Syncing revision " + state.revision;
  else if (state.status === "synced") status = "Synced revision " + state.revision;
  else if (state.status === "peer_left") status = "Peer left";
  else if (state.status === "error") status = "Failed: " + (state.error || "unknown error");
  if (state.draft_warning) return status + " - Draft warning: " + state.draft_warning;
  if (state.recent_warning) return status + " - Recent list warning: " + state.recent_warning;
  if (state.draft_recovered) return status + " - Recovered encrypted draft revision " + state.revision;
  return status;
}

function renderNote(state) {
  if (state.id !== lastNoteID) {
    lastNoteID = state.id;
    noteDirty = false;
    clearTimeout(noteDebounce);
    noteDebounce = null;
  }
  currentNote = state;
  if (!noteDirty && noteEditor.value !== (state.text || "")) {
    noteEditor.value = state.text || "";
  }
  if (state.code) noteCode.value = state.code;
  if (state.pad) notePad.value = state.pad;
  notePadSummary.textContent = "Pad: " + (state.pad || "main");
  notePairingCode.textContent = state.code || "";
  noteShareLink.value = state.link || "";
  noteShareArea.classList.toggle("hidden", !(state.host && state.code));
  noteStatus.textContent = noteStatusText(state);
  noteStatus.className = "note-status";
  if (state.status === "opening" || state.status === "waiting" || state.status === "connecting" || state.status === "reconnecting" || state.status === "syncing") {
    noteStatus.classList.add("running");
  } else if (state.status === "available" || state.status === "connected" || state.status === "synced") {
    noteStatus.classList.add("done");
  } else if (state.status === "error") {
    noteStatus.classList.add("failed");
  } else if (state.status === "peer_left") {
    noteStatus.classList.add("canceled");
  }
  syncControls();
  if (state.connected && noteRecentLoadedID !== state.id) {
    noteRecentLoadedID = state.id;
    refreshNoteRecents();
  }
}

async function start(path, body) {
  taskMessage.textContent = "Starting...";
  taskMessage.className = "task-message running";
  try {
    await api(path, { method: "POST", body: JSON.stringify(body) });
    await refreshJob();
  } catch (error) {
    taskMessage.textContent = "Failed: " + error.message;
    taskMessage.className = "task-message failed";
  }
}

async function refreshJob() {
  try {
    renderJob(await api("/api/job"));
  } catch (error) {
    taskMessage.textContent = error.message;
    taskMessage.className = "task-message failed";
  }
}

async function refreshNote() {
  try {
    renderNote(await api("/api/note"));
  } catch (error) {
    noteStatus.textContent = error.message;
    noteStatus.className = "note-status failed";
  }
}

async function startNote(path, body) {
  noteStatus.textContent = "Starting...";
  noteStatus.className = "note-status running";
  try {
    renderNote(await api(path, { method: "POST", body: JSON.stringify(body) }));
  } catch (error) {
    noteStatus.textContent = "Failed: " + error.message;
    noteStatus.className = "note-status failed";
  }
}

tabs.forEach((tab) => tab.addEventListener("click", () => selectTab(tab.dataset.tab)));

document.querySelector("#browseSendPath").addEventListener("click", () => {
  openPathBrowser(document.querySelector("#sendPath"), "send", "Choose file or folder");
});

document.querySelector("#browseOutputDir").addEventListener("click", () => {
  openPathBrowser(document.querySelector("#outputDir"), "directory", "Choose output folder");
});

document.querySelector("#closePathBrowser").addEventListener("click", () => pathBrowserDialog.close());
document.querySelector("#pathBrowserGo").addEventListener("click", () => loadPathBrowser(pathBrowserCurrent.value));
pathBrowserCurrent.addEventListener("keydown", (event) => {
  if (event.key === "Enter") {
    event.preventDefault();
    loadPathBrowser(pathBrowserCurrent.value);
  }
});
pathBrowserUp.addEventListener("click", () => {
  if (pathBrowserState.response && pathBrowserState.response.parent) {
    loadPathBrowser(pathBrowserState.response.parent);
  }
});
pathBrowserFilter.addEventListener("input", () => {
  if (pathBrowserState.response) renderPathBrowser(pathBrowserState.response);
});
pathBrowserSort.addEventListener("change", () => {
  const current = pathBrowserState.response ? pathBrowserState.response.current : pathBrowserCurrent.value;
  loadPathBrowser(current);
});
document.querySelector("#selectCurrentPath").addEventListener("click", () => {
  if (pathBrowserState.response) chooseBrowserPath(pathBrowserState.response.current);
});

document.querySelector("#sendPanel").addEventListener("submit", (event) => {
  event.preventDefault();
  start("/api/send", {
    path: document.querySelector("#sendPath").value,
    code: document.querySelector("#sendCode").value,
    symlinks: document.querySelector("#symlinks").value,
    no_gitignore: document.querySelector("#noGitIgnore").checked,
  });
});

document.querySelector("#recvPanel").addEventListener("submit", (event) => {
  event.preventDefault();
  start("/api/recv", {
    code: document.querySelector("#recvCode").value,
    output_dir: document.querySelector("#outputDir").value,
    on_conflict: document.querySelector("#conflict").value,
  });
});

document.querySelector("#doctorPanel").addEventListener("submit", (event) => {
  event.preventDefault();
  start("/api/doctor", { timeout: document.querySelector("#doctorTimeout").value });
});

hostNote.addEventListener("click", () => startNote("/api/note/host", { code: noteCode.value, pad: notePad.value }));

joinNote.addEventListener("click", () => {
  startNote("/api/note/join", { code: noteCode.value, pad: notePad.value });
});

noteEditor.addEventListener("input", () => {
  noteEditVersion++;
  noteDirty = true;
  clearTimeout(noteDebounce);
  noteStatus.textContent = "Editing";
  noteStatus.className = "note-status running";
  noteDebounce = setTimeout(publishNote, 250);
});

async function publishNote() {
  noteDebounce = null;
  const version = noteEditVersion;
  const text = noteEditor.value;
  try {
    const state = await api("/api/note/update", {
      method: "POST",
      body: JSON.stringify({ text }),
    });
    if (version === noteEditVersion) noteDirty = false;
    renderNote(state);
  } catch (error) {
    noteStatus.textContent = "Failed: " + error.message;
    noteStatus.className = "note-status failed";
  }
}

clearNote.addEventListener("click", async () => {
  clearTimeout(noteDebounce);
  noteDebounce = null;
  noteEditVersion++;
  const version = noteEditVersion;
  noteDirty = true;
  noteEditor.value = "";
  try {
    const state = await api("/api/note/clear", { method: "POST", body: "{}" });
    if (version === noteEditVersion) noteDirty = false;
    renderNote(state);
  } catch (error) {
    noteStatus.textContent = "Failed: " + error.message;
    noteStatus.className = "note-status failed";
  }
});

leaveNote.addEventListener("click", async () => {
  clearTimeout(noteDebounce);
  noteDebounce = null;
  noteDirty = false;
  try {
    renderNote(await api("/api/note/leave", { method: "POST", body: "{}" }));
  } catch (error) {
    noteStatus.textContent = "Failed: " + error.message;
    noteStatus.className = "note-status failed";
  }
});

cancelButton.addEventListener("click", async () => {
  await api("/api/job/cancel", { method: "POST", body: "{}" }).catch((error) => {
    taskMessage.textContent = error.message;
    taskMessage.className = "task-message failed";
  });
  await refreshJob();
});

document.querySelector("#copyLink").addEventListener("click", async () => {
  const value = shareLink.value || pairingCode.textContent;
  await navigator.clipboard.writeText(value);
  taskMessage.textContent = shareLink.value ? "Public link copied." : "Pairing code copied.";
  taskMessage.className = "task-message done";
});

document.querySelector("#copyNoteLink").addEventListener("click", async () => {
  const value = noteShareLink.value || notePairingCode.textContent;
  await navigator.clipboard.writeText(value);
  noteStatus.textContent = noteShareLink.value ? "Public link copied" : "Pairing code copied";
  noteStatus.className = "note-status done";
});

async function loadConfig() {
  try {
    const config = await api("/api/config");
    const route = config.local
      ? "LAN relay discovery"
      : config.relay
        ? (config.proxy ? "Native relay via proxy" : (config.no_direct ? "Native relay" : "Native direct with relay fallback"))
        : "WebRTC via " + config.signal;
    document.querySelector("#routeBadge").textContent = config.interface ? route + " via " + config.interface : route;
  } catch (error) {
    document.querySelector("#routeBadge").textContent = error.message;
  }
}

loadConfig();
refreshJob();
refreshNote();
refreshNoteRecents();
setInterval(refreshJob, 700);
setInterval(refreshNote, 500);
`
