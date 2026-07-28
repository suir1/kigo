#!/usr/bin/env python3
"""End-to-end persistence smoke test for the native shared notepad."""

from __future__ import annotations

import os
import queue
import re
import shutil
import socket
import subprocess
import sys
import tempfile
import threading
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


class Output:
    def __init__(self, process: subprocess.Popen[str]) -> None:
        self.process = process
        self.lines: list[str] = []
        self.events: queue.Queue[str] = queue.Queue()
        self.thread = threading.Thread(target=self._read, daemon=True)
        self.thread.start()

    def _read(self) -> None:
        assert self.process.stdout is not None
        for line in self.process.stdout:
            self.lines.append(line)
            self.events.put(line)

    def wait_for(self, needle: str, timeout: float = 8.0) -> str:
        deadline = time.time() + timeout
        while time.time() < deadline:
            joined = "".join(self.lines)
            if needle in joined:
                return joined
            if self.process.poll() is not None:
                break
            try:
                self.events.get(timeout=0.1)
            except queue.Empty:
                pass
        fail(f"timed out waiting for {needle!r}", self)
        return ""


def fail(message: str, *outputs: Output) -> None:
    print(f"FAIL: {message}", file=sys.stderr)
    for output in outputs:
        if output.lines:
            print("".join(output.lines)[-5000:], file=sys.stderr)
    raise SystemExit(1)


def find_free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


def wait_for_port(port: int, timeout: float = 5.0) -> None:
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            with socket.create_connection(("127.0.0.1", port), timeout=0.2):
                return
        except OSError:
            time.sleep(0.05)
    raise RuntimeError(f"service did not listen on 127.0.0.1:{port}")


def build_binary(work: Path) -> Path:
    configured = os.environ.get("KIGO_BIN")
    if configured:
        return Path(configured)
    binary = work / "kigo"
    subprocess.run(
        ["go", "build", "-o", str(binary), "./cmd/kigo"],
        cwd=ROOT,
        check=True,
    )
    return binary


def spawn(binary: Path, args: list[str]) -> tuple[subprocess.Popen[str], Output]:
    process = subprocess.Popen(
        [str(binary), *args],
        cwd=ROOT,
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
        bufsize=1,
    )
    return process, Output(process)


def send_line(process: subprocess.Popen[str], line: str) -> None:
    if process.stdin is None:
        raise RuntimeError("process stdin is unavailable")
    process.stdin.write(line + "\n")
    process.stdin.flush()


def stop(process: subprocess.Popen[str] | None) -> None:
    if process is None or process.poll() is not None:
        return
    process.terminate()
    try:
        process.wait(timeout=2)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait(timeout=2)


def main() -> None:
    work = Path(tempfile.mkdtemp(prefix="kigo-note-smoke-"))
    os.environ["KIGO_NOTE_DRAFT_PATH"] = str(work / "note-drafts")
    os.environ["KIGO_CONFIG_PATH"] = str(work / "config.json")
    service: subprocess.Popen[str] | None = None
    host: subprocess.Popen[str] | None = None
    join: subprocess.Popen[str] | None = None
    try:
        binary = build_binary(work)
        port = find_free_port()
        listen = f"127.0.0.1:{port}"
        base_url = f"http://{listen}"
        service, service_output = spawn(
            binary,
            [
                "serve",
                "--listen",
                listen,
                "--public-url",
                base_url,
                "--note-store",
                str(work / "persistent-notes"),
            ],
        )
        wait_for_port(port)
        common = [
            "--signal",
            base_url,
            "--web-url",
            base_url,
            "--route-history",
            str(work / "route-history.json"),
        ]

        code = "NATIVE-NOTE-DRAFT-2026"
        host, host_output = spawn(binary, [*common, "note", "host", "--code", code])
        host_text = host_output.wait_for("Opening persistent notepad...")
        if not re.search(rf"^Code: {code}$", host_text, re.MULTILINE):
            fail("host did not print a pairing code", host_output)
        host_output.wait_for("Connected. Pad: main")

        send_line(host, "hello from host")
        host_output.wait_for("Synced revision 1")
        send_line(host, "/quit")
        host.wait(timeout=5)
        if host.returncode != 0:
            fail(f"host returncode={host.returncode}", host_output)

        join, join_output = spawn(binary, [*common, "note", "join", code])
        join_output.wait_for("Recovered revision 1:\nhello from host")
        join_output.wait_for("Connected. Pad: main")
        send_line(join, "reply from join")
        join_output.wait_for("Synced revision 2")
        send_line(join, "/quit")
        join.wait(timeout=5)
        if join.returncode != 0:
            fail(f"join returncode={join.returncode}", join_output)

        stop(service)
        service, service_output = spawn(
            binary,
            [
                "serve",
                "--listen",
                listen,
                "--public-url",
                base_url,
                "--note-store",
                str(work / "persistent-notes"),
            ],
        )
        wait_for_port(port)
        host, host_output = spawn(binary, [*common, "note", "host", "--code", code])
        host_output.wait_for("Recovered revision 2:\nreply from join")
        host_output.wait_for("Connected. Pad: main")
        send_line(host, "/show")
        host_output.wait_for("Local revision 2:\nreply from join")
        send_line(host, "/quit")
        host.wait(timeout=5)
        if host.returncode != 0:
            fail(f"restored host returncode={host.returncode}", host_output)
        print("all persistent notepad smoke checks passed")
    finally:
        stop(join)
        stop(host)
        stop(service)
        shutil.rmtree(work, ignore_errors=True)


if __name__ == "__main__":
    main()
