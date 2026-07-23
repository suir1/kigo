#!/usr/bin/env python3
"""Build and sanitize Kigo network-matrix diagnostic artifacts."""

from __future__ import annotations

import argparse
import datetime as dt
import json
import os
import re
import sys
from pathlib import Path
from typing import Any
from urllib.parse import urlparse

SCHEMA_VERSION = 1
ROOM_TOKEN_RE = re.compile(r"\b[0-9a-fA-F]{64}\b")
SENSITIVE_KEY_RE = re.compile(
    r"(?:^|_)(?:code|credential|pass|password|secret|token|username)(?:_|$)",
    re.IGNORECASE,
)
ROUTE_RE = re.compile(
    r"^(?:Route|Route fallback|Route negotiation):\s+"
    r"(direct via signaling|direct|relay|WebRTC)\b",
    re.MULTILINE | re.IGNORECASE,
)


def utc_now() -> str:
    return dt.datetime.now(dt.timezone.utc).replace(microsecond=0).isoformat()


def read_text(path: str | None) -> str:
    if not path:
        return ""
    try:
        return Path(path).read_text(encoding="utf-8", errors="replace")
    except FileNotFoundError:
        return ""


def write_json(path: str | None, value: Any) -> None:
    text = json.dumps(value, indent=2, sort_keys=True) + "\n"
    if path:
        Path(path).parent.mkdir(parents=True, exist_ok=True)
        Path(path).write_text(text, encoding="utf-8")
    else:
        sys.stdout.write(text)


def redact_value(value: Any, key: str = "") -> Any:
    if key and SENSITIVE_KEY_RE.search(key) and not key.endswith(("_set", "_mode")):
        if isinstance(value, bool):
            return value
        return "[REDACTED]"
    if isinstance(value, dict):
        return {item_key: redact_value(item, item_key) for item_key, item in value.items()}
    if isinstance(value, list):
        return [redact_value(item) for item in value]
    if isinstance(value, str):
        return redact_text(value)
    return value


def redact_text(text: str, code: str = "") -> str:
    if code:
        text = text.replace(code, "[REDACTED_CODE]")
    text = re.sub(r"(#c=)[A-HJ-NP-Z2-9]{6}", r"\1[REDACTED_CODE]", text)
    text = re.sub(
        r"(?im)^(\s*(?:Code|Joining room):\s*)[A-HJ-NP-Z2-9]{6}\s*$",
        r"\1[REDACTED_CODE]",
        text,
    )
    text = ROOM_TOKEN_RE.sub("[REDACTED_ROOM_TOKEN]", text)
    return text


def selected_route(*logs: str) -> str:
    matches: list[str] = []
    for text in logs:
        matches.extend(match.group(1).lower() for match in ROUTE_RE.finditer(text))
    if not matches:
        return "unknown"
    route = matches[-1]
    if route.startswith("direct"):
        return "direct"
    if route == "webrtc":
        return "webrtc"
    return route


def direct_observations(*logs: str) -> dict[str, list[str]]:
    advertised: set[str] = set()
    public: set[str] = set()
    for text in logs:
        for line in text.splitlines():
            if line.startswith("Direct: "):
                advertised.update(
                    candidate.strip()
                    for candidate in line.removeprefix("Direct: ").split(",")
                    if candidate.strip()
                )
            elif line.startswith("TCP public probe: "):
                candidate = line.removeprefix("TCP public probe: ").strip()
                if candidate:
                    public.add(candidate)
    return {
        "advertised": sorted(advertised),
        "relay_observed_public": sorted(public),
    }


def status_from_args(args: argparse.Namespace, route: str) -> tuple[str, list[str]]:
    reasons: list[str] = []
    if args.skipped:
        return "skipped", [args.reason or "scenario was not available"]
    if args.sender_exit != 0:
        reasons.append(f"sender exited with {args.sender_exit}")
    if args.receiver_exit != 0:
        reasons.append(f"receiver exited with {args.receiver_exit}")
    if not args.input_sha256 or not args.output_sha256:
        reasons.append("checksum unavailable")
    elif args.input_sha256 != args.output_sha256:
        reasons.append("checksum mismatch")
    if args.expected_route != "any" and route != args.expected_route:
        reasons.append(f"selected route {route!r}, expected {args.expected_route!r}")
    return ("failed", reasons) if reasons else ("passed", [])


def command_redact_log(args: argparse.Namespace) -> int:
    Path(args.output).write_text(
        redact_text(read_text(args.input), args.code),
        encoding="utf-8",
    )
    return 0


def command_redact_json(args: argparse.Namespace) -> int:
    try:
        value = json.loads(read_text(args.input))
    except json.JSONDecodeError as exc:
        raise SystemExit(f"{args.input}: invalid JSON: {exc}") from exc
    write_json(args.output, redact_value(value))
    return 0


def command_scenario(args: argparse.Namespace) -> int:
    sender_log = read_text(args.sender_log) + "\n" + read_text(args.sender_error)
    receiver_log = read_text(args.receiver_log) + "\n" + read_text(args.receiver_error)
    route = "unknown" if args.skipped else selected_route(sender_log, receiver_log)
    status, reasons = status_from_args(args, route)
    artifacts = {}
    for name in (
        "sender_log",
        "sender_error",
        "receiver_log",
        "receiver_error",
        "sender_doctor",
        "receiver_doctor",
        "sender_route",
        "receiver_route",
    ):
        value = getattr(args, name, "")
        if value:
            artifacts[name] = os.path.basename(value)
    report = {
        "schema_version": SCHEMA_VERSION,
        "name": args.name,
        "network_model": args.network_model,
        "status": status,
        "expected_route": args.expected_route,
        "selected_route": route,
        "duration_ms": max(0, args.duration_ms),
        "processes": {
            "sender_exit": args.sender_exit,
            "receiver_exit": args.receiver_exit,
        },
        "checksums": {
            "input_sha256": args.input_sha256,
            "output_sha256": args.output_sha256,
            "match": bool(args.input_sha256)
            and args.input_sha256 == args.output_sha256,
        },
        "direct": direct_observations(sender_log, receiver_log),
        "artifacts": artifacts,
    }
    if reasons:
        report["reasons"] = reasons
    if args.note:
        report["notes"] = args.note
    write_json(args.output, report)
    return 0 if status != "failed" else 1


def command_combine(args: argparse.Namespace) -> int:
    scenarios = []
    for path in args.scenarios:
        try:
            scenarios.append(json.loads(read_text(path)))
        except json.JSONDecodeError as exc:
            raise SystemExit(f"{path}: invalid JSON: {exc}") from exc
    metadata: dict[str, Any] = {}
    if args.metadata:
        try:
            metadata = json.loads(args.metadata)
        except json.JSONDecodeError as exc:
            raise SystemExit(f"--metadata: invalid JSON: {exc}") from exc
    counts = {
        state: sum(1 for scenario in scenarios if scenario.get("status") == state)
        for state in ("passed", "failed", "skipped")
    }
    report = {
        "schema_version": SCHEMA_VERSION,
        "kind": args.kind,
        "generated_at": utc_now(),
        "status": "failed" if counts["failed"] else "passed",
        "counts": counts,
        "metadata": redact_value(metadata),
        "scenarios": scenarios,
    }
    write_json(args.output, report)
    return 1 if counts["failed"] else 0


def validate_http_url(value: str, name: str) -> None:
    parsed = urlparse(value)
    if parsed.scheme not in ("http", "https") or not parsed.netloc:
        raise ValueError(f"{name} must be an absolute http or https URL")
    if parsed.username or parsed.password:
        raise ValueError(f"{name} must not contain embedded credentials")


def validate_relay(value: str) -> None:
    if not value:
        return
    host, separator, port_text = value.rpartition(":")
    if not separator or not host:
        raise ValueError("relay must use host:port")
    if host.startswith("[") and not host.endswith("]"):
        raise ValueError("relay has an invalid bracketed IPv6 address")
    try:
        port = int(port_text)
    except ValueError as exc:
        raise ValueError("relay port must be numeric") from exc
    if port < 1 or port > 65535:
        raise ValueError("relay port must be between 1 and 65535")


def command_validate_public(args: argparse.Namespace) -> int:
    errors: list[str] = []
    for name, value in (
        ("sender host", args.sender_host),
        ("receiver host", args.receiver_host),
        ("remote binary", args.remote_bin),
    ):
        if not value.strip():
            errors.append(f"{name} is required")
        elif name.endswith("host") and (
            value.startswith("-") or any(character.isspace() for character in value)
        ):
            errors.append(f"{name} must be one SSH destination without whitespace")
    try:
        validate_http_url(args.signal_url, "signal URL")
    except ValueError as exc:
        errors.append(str(exc))
    try:
        validate_relay(args.relay)
    except ValueError as exc:
        errors.append(str(exc))
    if args.payload_bytes < 1:
        errors.append("payload bytes must be positive")
    if args.timeout_seconds < 1:
        errors.append("timeout seconds must be positive")
    if args.expected_route not in ("any", "direct", "relay", "webrtc"):
        errors.append("expected route must be any, direct, relay, or webrtc")
    report = {
        "schema_version": SCHEMA_VERSION,
        "valid": not errors,
        "sender": args.sender_label,
        "receiver": args.receiver_label,
        "signal_url": args.signal_url,
        "relay": args.relay,
        "remote_bin": args.remote_bin,
        "payload_bytes": args.payload_bytes,
        "timeout_seconds": args.timeout_seconds,
        "expected_route": args.expected_route,
        "relay_password_configured": bool(args.relay_pass),
        "errors": errors,
    }
    write_json(args.output, report)
    return 0 if not errors else 2


def parser() -> argparse.ArgumentParser:
    root = argparse.ArgumentParser(description=__doc__)
    commands = root.add_subparsers(dest="command", required=True)

    redact_log = commands.add_parser("redact-log")
    redact_log.add_argument("--input", required=True)
    redact_log.add_argument("--output", required=True)
    redact_log.add_argument("--code", default="")
    redact_log.set_defaults(func=command_redact_log)

    redact_json = commands.add_parser("redact-json")
    redact_json.add_argument("--input", required=True)
    redact_json.add_argument("--output", required=True)
    redact_json.set_defaults(func=command_redact_json)

    scenario = commands.add_parser("scenario")
    scenario.add_argument("--name", required=True)
    scenario.add_argument("--network-model", required=True)
    scenario.add_argument(
        "--expected-route",
        choices=("any", "direct", "relay", "webrtc"),
        default="any",
    )
    scenario.add_argument("--sender-exit", type=int, default=0)
    scenario.add_argument("--receiver-exit", type=int, default=0)
    scenario.add_argument("--input-sha256", default="")
    scenario.add_argument("--output-sha256", default="")
    scenario.add_argument("--duration-ms", type=int, default=0)
    scenario.add_argument("--sender-log", default="")
    scenario.add_argument("--sender-error", default="")
    scenario.add_argument("--receiver-log", default="")
    scenario.add_argument("--receiver-error", default="")
    scenario.add_argument("--sender-doctor", default="")
    scenario.add_argument("--receiver-doctor", default="")
    scenario.add_argument("--sender-route", default="")
    scenario.add_argument("--receiver-route", default="")
    scenario.add_argument("--skipped", action="store_true")
    scenario.add_argument("--reason", default="")
    scenario.add_argument("--note", action="append", default=[])
    scenario.add_argument("--output", required=True)
    scenario.set_defaults(func=command_scenario)

    combine = commands.add_parser("combine")
    combine.add_argument("--kind", required=True)
    combine.add_argument("--metadata", default="")
    combine.add_argument("--output", required=True)
    combine.add_argument("scenarios", nargs="+")
    combine.set_defaults(func=command_combine)

    validate = commands.add_parser("validate-public")
    validate.add_argument("--sender-host", required=True)
    validate.add_argument("--receiver-host", required=True)
    validate.add_argument("--sender-label", default="sender")
    validate.add_argument("--receiver-label", default="receiver")
    validate.add_argument("--signal-url", required=True)
    validate.add_argument("--relay", default="")
    validate.add_argument("--relay-pass", default="")
    validate.add_argument("--remote-bin", default="kigo")
    validate.add_argument("--payload-bytes", type=int, default=1048576)
    validate.add_argument("--timeout-seconds", type=int, default=90)
    validate.add_argument("--expected-route", default="any")
    validate.add_argument("--output")
    validate.set_defaults(func=command_validate_public)
    return root


def main() -> int:
    args = parser().parse_args()
    return args.func(args)


if __name__ == "__main__":
    raise SystemExit(main())
