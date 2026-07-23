#!/usr/bin/env python3
"""PTY smoke tests for the Kigo native TUI."""

from __future__ import annotations

import fcntl
import os
import pty
import re
import select
import shutil
import socket
import struct
import subprocess
import sys
import tempfile
import termios
import threading
import time
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
README = ROOT / "README.md"
CONFIG_ROOT: Path | None = None


class CapturedNative:
    def __init__(self, args: list[str]) -> None:
        self.lines: list[str] = []
        self.process = subprocess.Popen(
            args,
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            cwd=ROOT,
            text=True,
            bufsize=1,
        )
        self.reader = threading.Thread(target=self._read, daemon=True)
        self.reader.start()

    def _read(self) -> None:
        assert self.process.stdout is not None
        for line in self.process.stdout:
            self.lines.append(line)

    def output(self) -> str:
        return "".join(self.lines)

    def write(self, text: str) -> None:
        if self.process.stdin is None:
            raise RuntimeError("native note stdin is unavailable")
        self.process.stdin.write(text)
        self.process.stdin.flush()

    def wait_for(self, needle: str, timeout: float = 12.0) -> None:
        deadline = time.time() + timeout
        while time.time() < deadline:
            if needle in self.output():
                return
            if self.process.poll() is not None:
                break
            time.sleep(0.05)
        raise RuntimeError(f"timed out waiting for {needle!r}\n{self.output()}")

    def wait(self, timeout: float = 12.0) -> None:
        code = self.process.wait(timeout=timeout)
        self.reader.join(timeout=1)
        if code != 0:
            raise RuntimeError(
                f"native note exited with status {code}\n{self.output()}"
            )

    def close(self) -> None:
        if self.process.poll() is None:
            self.process.terminate()
            try:
                self.process.wait(timeout=2)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait(timeout=2)


def strip_ansi(data: bytes) -> str:
    text = data.decode("utf-8", errors="replace")
    text = re.sub(r"\x1b\[[0-9;?]*[ -/]*[@-~]", "", text)
    text = re.sub(r"\x1b\][^\x07]*(?:\x07|\x1b\\)", "", text)
    return text


def set_size(fd: int, rows: int = 38, columns: int = 110) -> None:
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", rows, columns, 0, 0))


def spawn(binary: Path, *args: str) -> tuple[subprocess.Popen[bytes], int]:
    if CONFIG_ROOT is None:
        raise RuntimeError("TUI smoke config directory is not initialized")
    master, slave = pty.openpty()
    set_size(slave)
    env = os.environ.copy()
    env["TERM"] = "xterm-256color"
    env["KIGO_CONFIG_PATH"] = str(CONFIG_ROOT / f"{time.time_ns()}.json")
    process = subprocess.Popen(
        [str(binary), *args],
        stdin=slave,
        stdout=slave,
        stderr=slave,
        cwd=ROOT,
        env=env,
        close_fds=True,
    )
    os.close(slave)
    return process, master


def read_available(fd: int, timeout: float = 1.5) -> bytes:
    output = bytearray()
    deadline = time.time() + timeout
    while time.time() < deadline:
        ready, _, _ = select.select([fd], [], [], 0.1)
        if fd not in ready:
            if output:
                break
            continue
        try:
            chunk = os.read(fd, 32768)
        except OSError:
            break
        if not chunk:
            break
        output.extend(chunk)
    return bytes(output)


def wait_for(fd: int, needle: str, timeout: float = 5.0) -> bytes:
    output = bytearray()
    deadline = time.time() + timeout
    while time.time() < deadline:
        output.extend(read_available(fd, 0.4))
        if needle in strip_ansi(bytes(output)):
            return bytes(output)
    fail(f"timed out waiting for {needle!r}", bytes(output))
    return b""


def stop(process: subprocess.Popen[bytes], master: int) -> None:
    if process.poll() is None:
        os.write(master, b"\x03")
        try:
            process.wait(timeout=2)
        except subprocess.TimeoutExpired:
            process.terminate()
            try:
                process.wait(timeout=2)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=2)
    os.close(master)


def fail(message: str, output: bytes = b"") -> None:
    print(f"FAIL: {message}", file=sys.stderr)
    if output:
        print(strip_ansi(output)[-5000:], file=sys.stderr)
    raise SystemExit(1)


def require_text(output: bytes, *needles: str) -> None:
    plain = strip_ansi(output)
    for needle in needles:
        if needle not in plain:
            fail(f"TUI output missing {needle!r}", output)


def test_menu(binary: Path) -> None:
    process, master = spawn(binary, "tui")
    output = wait_for(master, "Start sending")
    require_text(output, "KIGO", "native transfer console", "Send", "Receive", "Doctor", "Notepad")
    if process.poll() is not None:
        fail("TUI menu exited before user input", output)
    stop(process, master)
    print("ok tui menu")


def test_doctor(binary: Path) -> None:
    process, master = spawn(binary, "--signal", "http://127.0.0.1:1", "tui")
    output = wait_for(master, "Start sending")
    os.write(master, b"\x1b[C\x1b[C\t\t\r")
    output += wait_for(master, "Network doctor", 5.0)
    require_text(output, "Network doctor", "Status:")
    stop(process, master)
    print("ok tui doctor")


def test_send_and_cancel(binary: Path) -> None:
    process, master = spawn(binary, "--signal", "http://127.0.0.1:1", "tui")
    output = wait_for(master, "Start sending")
    os.write(master, b"\t" + str(README).encode() + b"\tTUI-CUSTOM-2026\t\t\t\r")
    output += wait_for(master, "Pairing code:", 5.0)
    require_text(output, "Sending", "Pairing code:", "Public link:")
    if not re.search(r"Pairing code:\s+TUI-CUSTOM-2026", strip_ansi(output)):
        fail("TUI did not render the custom pairing code", output)
    os.write(master, b"c")
    output += read_available(master, 1.0)
    stop(process, master)
    print("ok tui send and cancel")


def test_path_browser(binary: Path) -> None:
    process, master = spawn(binary, "tui")
    output = wait_for(master, "Start sending")
    os.write(master, b"\t\r")
    output += wait_for(master, "Choose file or folder")
    os.write(master, b"README.md\x1b[B\r")
    output += wait_for(master, str(README))
    require_text(output, "Choose file or folder", "Filter:", "Sort:", str(README))
    stop(process, master)
    print("ok tui path browser")


def free_service_url() -> str:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as probe:
        probe.bind(("127.0.0.1", 0))
        port = probe.getsockname()[1]
    return f"http://127.0.0.1:{port}"


def start_service(binary: Path) -> tuple[subprocess.Popen[bytes], str]:
    url = free_service_url()
    process = subprocess.Popen(
        [str(binary), "serve", "--listen", url.removeprefix("http://")],
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        cwd=ROOT,
    )
    deadline = time.time() + 8.0
    while time.time() < deadline:
        if process.poll() is not None:
            fail("TUI smoke signaling service exited during startup")
        try:
            with urllib.request.urlopen(url + "/api/health", timeout=0.5):
                return process, url
        except OSError:
            time.sleep(0.05)
    process.terminate()
    process.wait(timeout=2)
    fail("timed out starting TUI smoke signaling service")
    raise AssertionError("unreachable")


def test_notepad(binary: Path, service_url: str) -> None:
    process, master = spawn(
        binary,
        "--signal",
        service_url,
        "--web-url",
        service_url,
        "tui",
    )
    native: CapturedNative | None = None
    pad = "TUI Sprint Notes"
    output = wait_for(master, "Start sending")
    try:
        os.write(master, b"\x1b[D\t\tTUI-NOTE-2026\t\x15" + pad.encode() + b"\t\r")
        output += wait_for(master, "Pairing code:", 8.0)
        plain = strip_ansi(output)
        match = re.search(r"Pairing code:\s+(TUI-NOTE-2026)", plain)
        if match is None:
            fail("TUI notepad did not render a pairing code", output)
        code = match.group(1)
        require_text(output, "Shared notepad", "waiting for peer", "Public link", "Pad:", pad)

        native = CapturedNative(
            [
                str(binary),
                "--signal",
                service_url,
                "--web-url",
                service_url,
                "note",
                "--pad",
                pad,
                "join",
                code,
            ]
        )
        output += wait_for(master, "connected", 15.0)
        require_text(output, "Ctrl+S sync now", "Ctrl+L clear")
        native.wait_for(f"Connected. Pad: {pad}", 15.0)

        os.write(master, b"tui first line\rtui second line")
        native.wait_for("tui first line\ntui second line", 15.0)

        native.write("native reply to tui\n")
        output += wait_for(master, "native reply to tui", 15.0)

        os.write(master, b"\x0c")
        native.wait_for("(empty)", 15.0)

        os.write(master, b"\x1b")
        native.wait(15.0)
        output += wait_for(master, "closed", 5.0)
        require_text(output, "Enter/Esc return")
    except RuntimeError as exc:
        fail(str(exc), output)
    finally:
        if native is not None:
            native.close()
        stop(process, master)
    print("ok tui notepad")


def build_binary() -> tuple[Path, Path | None]:
    configured = os.environ.get("KIGO_BIN")
    if configured:
        return Path(configured), None
    work = Path(tempfile.mkdtemp(prefix="kigo-tui-smoke-"))
    binary = work / "kigo"
    subprocess.run(
        ["go", "build", "-o", str(binary), "./cmd/kigo"],
        cwd=ROOT,
        check=True,
    )
    return binary, work


def main() -> None:
    global CONFIG_ROOT
    if os.name == "nt":
        print("TUI smoke requires a Unix PTY; skipped on Windows")
        return
    binary, work = build_binary()
    config_work = Path(tempfile.mkdtemp(prefix="kigo-tui-config-"))
    CONFIG_ROOT = config_work
    os.environ["KIGO_NOTE_DRAFT_PATH"] = str(config_work / "note-drafts")
    os.environ["KIGO_CONFIG_PATH"] = str(config_work / "shared-config.json")
    if not binary.is_file():
        fail(f"Kigo binary not found: {binary}")
    try:
        test_menu(binary)
        test_doctor(binary)
        test_send_and_cancel(binary)
        test_path_browser(binary)
        service, service_url = start_service(binary)
        try:
            test_notepad(binary, service_url)
        finally:
            if service.poll() is None:
                service.terminate()
                try:
                    service.wait(timeout=2)
                except subprocess.TimeoutExpired:
                    service.kill()
                    service.wait(timeout=2)
        print("all TUI smoke checks passed")
    finally:
        shutil.rmtree(config_work, ignore_errors=True)
        if work is not None:
            shutil.rmtree(work, ignore_errors=True)


if __name__ == "__main__":
    main()
