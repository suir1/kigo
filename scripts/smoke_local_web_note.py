#!/usr/bin/env python3

import json
import re
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request


TIMEOUT = 20.0


class NativeNote:
    def __init__(self, args):
        self.lines = []
        self.process = subprocess.Popen(
            args,
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
            bufsize=1,
        )
        self.reader = threading.Thread(target=self._read, daemon=True)
        self.reader.start()

    def _read(self):
        assert self.process.stdout is not None
        for line in self.process.stdout:
            self.lines.append(line)

    def output(self):
        return "".join(self.lines)

    def write(self, text):
        if self.process.stdin is None:
            raise RuntimeError("native note stdin is unavailable")
        self.process.stdin.write(text)
        self.process.stdin.flush()

    def wait(self):
        try:
            code = self.process.wait(timeout=TIMEOUT)
        except subprocess.TimeoutExpired as exc:
            raise RuntimeError("native note did not exit\n" + self.output()) from exc
        self.reader.join(timeout=1)
        if code != 0:
            raise RuntimeError(
                f"native note exited with status {code}\n{self.output()}"
            )

    def close(self):
        if self.process.poll() is None:
            self.process.terminate()
            try:
                self.process.wait(timeout=3)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait(timeout=3)


def api(local_url, token, path, body=None):
    data = None
    method = "GET"
    if body is not None:
        data = json.dumps(body).encode("utf-8")
        method = "POST"
    request = urllib.request.Request(
        local_url + path,
        data=data,
        method=method,
        headers={
            "Content-Type": "application/json",
            "X-Kigo-Token": token,
        },
    )
    try:
        with urllib.request.urlopen(request, timeout=5) as response:
            return json.load(response)
    except urllib.error.HTTPError as exc:
        message = exc.read().decode("utf-8", errors="replace")
        raise RuntimeError(f"{method} {path} failed: {exc.code} {message}") from exc


def wait_for(label, condition):
    deadline = time.monotonic() + TIMEOUT
    while time.monotonic() < deadline:
        value = condition()
        if value:
            return value
        time.sleep(0.05)
    raise RuntimeError("timed out waiting for " + label)


def wait_note(local_url, token, condition, label):
    return wait_for(
        label,
        lambda: (state if condition(state := api(local_url, token, "/api/note")) else None),
    )


def native_args(binary, service_url, *args):
    return [
        binary,
        "--signal",
        service_url,
        "--web-url",
        service_url,
        *args,
    ]


def local_host_to_native(binary, service_url, local_url, token):
    pad = "Local Host Notes"
    started = api(
        local_url,
        token,
        "/api/note/host",
        {"code": "local-note-2026", "pad": pad},
    )
    code = started.get("code", "")
    if code != "LOCAL-NOTE-2026":
        raise RuntimeError("local host returned invalid code: " + repr(code))
    if started.get("pad") != pad or "p=Local+Host+Notes" not in started.get("link", ""):
        raise RuntimeError("local host returned invalid pad/link: " + repr(started))
    native = NativeNote(
        native_args(binary, service_url, "note", "--pad", pad, "join", code)
    )
    try:
        wait_note(
            local_url,
            token,
            lambda state: state.get("connected") and state.get("pad") == pad,
            "local host connection",
        )
        wait_for("native local host pad", lambda: f"Connected. Pad: {pad}" in native.output())

        local_text = "local web first line\nlocal web second line"
        api(local_url, token, "/api/note/update", {"text": local_text})
        wait_for("native receiving local text", lambda: local_text in native.output())
        wait_note(
            local_url,
            token,
            lambda state: state.get("synced") and state.get("revision") == 1,
            "local update acknowledgement",
        )

        native.write("native reply to local web\n")
        wait_note(
            local_url,
            token,
            lambda state: state.get("text") == "native reply to local web",
            "native update at local web",
        )

        api(local_url, token, "/api/note/clear", {})
        wait_for("native receiving clear", lambda: "(empty)" in native.output())
        persisted_text = "local encrypted draft survives leave"
        api(local_url, token, "/api/note/update", {"text": persisted_text})
        wait_for("native receiving persisted draft", lambda: persisted_text in native.output())
        api(local_url, token, "/api/note/leave", {})
        native.wait()
        restored = api(
            local_url,
            token,
            "/api/note/host",
            {"code": code, "pad": pad},
        )
        if not restored.get("draft_recovered") or restored.get("text") != persisted_text:
            raise RuntimeError("local host did not restore encrypted draft: " + repr(restored))
        api(local_url, token, "/api/note/leave", {})
    finally:
        native.close()


def native_host_to_local(binary, service_url, local_url, token):
    pad = "Local Join Notes"
    native = NativeNote(
        native_args(
            binary,
            service_url,
            "note",
            "--pad",
            pad,
            "host",
            "--code",
            "native-note-2026",
            "--no-qrcode",
        )
    )
    try:
        match = wait_for(
            "native host pairing code",
            lambda: re.search(r"Code:\s*(NATIVE-NOTE-2026)", native.output()),
        )
        code = match.group(1)
        joined = api(local_url, token, "/api/note/join", {"code": code, "pad": pad})
        if joined.get("pad") != pad:
            raise RuntimeError("local join returned invalid pad: " + repr(joined))
        wait_note(
            local_url,
            token,
            lambda state: state.get("connected") and state.get("pad") == pad,
            "local join connection",
        )
        wait_for("native local join pad", lambda: f"Connected. Pad: {pad}" in native.output())

        native.write("native host document\n")
        wait_note(
            local_url,
            token,
            lambda state: state.get("text") == "native host document",
            "native host update at local web",
        )

        api(local_url, token, "/api/note/update", {"text": "local join document"})
        wait_for(
            "native host receiving local update",
            lambda: "local join document" in native.output(),
        )
        api(local_url, token, "/api/note/leave", {})
        native.wait()
    finally:
        native.close()


def main():
    if len(sys.argv) != 5:
        raise SystemExit(
            "usage: smoke_local_web_note.py <kigo> <service-url> <local-url> <token>"
        )
    binary, service_url, local_url, token = sys.argv[1:]
    local_host_to_native(binary, service_url, local_url, token)
    native_host_to_local(binary, service_url, local_url, token)
    print("all local web notepad smoke checks passed")


if __name__ == "__main__":
    main()
