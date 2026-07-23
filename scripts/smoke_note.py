#!/usr/bin/env python3
"""End-to-end relay smoke test for the native shared notepad."""

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
    raise RuntimeError(f"relay did not listen on 127.0.0.1:{port}")


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
    relay: subprocess.Popen[str] | None = None
    host: subprocess.Popen[str] | None = None
    join: subprocess.Popen[str] | None = None
    try:
        binary = build_binary(work)
        port = find_free_port()
        relay_addr = f"127.0.0.1:{port}"
        relay = subprocess.Popen(
            [str(binary), "relay", "--listen", relay_addr, "--no-lan-announce"],
            cwd=ROOT,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
        )
        wait_for_port(port)
        common = [
            "--transport",
            "native",
            "--relay",
            relay_addr,
            "--no-direct",
            "--connections",
            "1",
            "--route-history",
            str(work / "route-history.json"),
        ]

        code = "NATIVE-NOTE-DRAFT-2026"
        host, host_output = spawn(binary, [*common, "note", "host", "--code", code])
        host_text = host_output.wait_for("Waiting for peer...")
        if not re.search(rf"^Code: {code}$", host_text, re.MULTILINE):
            fail("host did not print a pairing code", host_output)

        join, join_output = spawn(binary, [*common, "note", "join", code])
        join_output.wait_for("Connected. Pad: main")
        host_output.wait_for("Connected. Pad: main")

        send_line(host, "hello from host")
        join_output.wait_for("Remote revision 1:\nhello from host")
        host_output.wait_for("Synced revision 1")

        send_line(join, "reply from join")
        host_output.wait_for("Remote revision 2:\nreply from join")
        join_output.wait_for("Synced revision 2")

        send_line(host, "/quit")
        join_output.wait_for("Peer closed the notepad.")
        host.wait(timeout=5)
        join.wait(timeout=5)
        if host.returncode != 0 or join.returncode != 0:
            fail(
                f"host returncode={host.returncode} join returncode={join.returncode}",
                host_output,
                join_output,
            )

        host, host_output = spawn(binary, [*common, "note", "host", "--code", code])
        host_output.wait_for("Waiting for peer...")
        join, join_output = spawn(binary, [*common, "note", "join", code])
        host_output.wait_for("Recovered encrypted draft revision 2.")
        join_output.wait_for("Recovered encrypted draft revision 2.")
        host_output.wait_for("Connected. Pad: main")
        join_output.wait_for("Connected. Pad: main")
        send_line(host, "/show")
        host_output.wait_for("Local revision 2:\nreply from join")
        send_line(host, "/quit")
        join_output.wait_for("Peer closed the notepad.")
        host.wait(timeout=5)
        join.wait(timeout=5)
        if host.returncode != 0 or join.returncode != 0:
            fail(
                f"restored host returncode={host.returncode} join returncode={join.returncode}",
                host_output,
                join_output,
            )
        print("all shared notepad smoke checks passed")
    finally:
        stop(join)
        stop(host)
        stop(relay)
        shutil.rmtree(work, ignore_errors=True)


if __name__ == "__main__":
    main()
