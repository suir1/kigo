#!/usr/bin/env python3
"""PTY smoke test for automatic terminal QR rendering."""

from __future__ import annotations

import os
import pty
import select
import subprocess
import tempfile
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


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


def command(binary: Path, no_qrcode: bool = False) -> list[str]:
    args = [
        str(binary),
        "--transport",
        "native",
        "--relay",
        "127.0.0.1:1",
        "--no-direct",
        "text",
        "send",
        "qr smoke",
    ]
    if no_qrcode:
        args.append("--no-qrcode")
    return args


def run_pty(args: list[str]) -> tuple[int, str]:
    master, slave = pty.openpty()
    process = subprocess.Popen(
        args,
        cwd=ROOT,
        stdin=subprocess.DEVNULL,
        stdout=slave,
        stderr=slave,
        close_fds=True,
    )
    os.close(slave)
    output = bytearray()
    deadline = time.time() + 10
    try:
        while time.time() < deadline:
            ready, _, _ = select.select([master], [], [], 0.1)
            if ready:
                try:
                    chunk = os.read(master, 65536)
                except OSError:
                    chunk = b""
                if not chunk:
                    break
                output.extend(chunk)
            if process.poll() is not None and not ready:
                break
        if process.poll() is None:
            process.kill()
        return process.wait(timeout=2), output.decode("utf-8", errors="replace")
    finally:
        os.close(master)


def main() -> None:
    with tempfile.TemporaryDirectory(prefix="kigo-qr-smoke-") as directory:
        binary = build_binary(Path(directory))

        _, terminal_output = run_pty(command(binary))
        if "QR:\r\n" not in terminal_output or "██" not in terminal_output:
            raise RuntimeError(f"TTY sender did not render QR code:\n{terminal_output}")

        _, disabled_output = run_pty(command(binary, no_qrcode=True))
        if "QR:" in disabled_output or "██" in disabled_output:
            raise RuntimeError(f"--no-qrcode still rendered QR code:\n{disabled_output}")

        redirected = subprocess.run(
            command(binary),
            cwd=ROOT,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
            timeout=10,
            check=False,
        )
        if "QR:" in redirected.stdout or "██" in redirected.stdout:
            raise RuntimeError(f"redirected sender rendered QR code:\n{redirected.stdout}")

    print("all terminal QR smoke checks passed")


if __name__ == "__main__":
    main()
