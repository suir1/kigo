#!/usr/bin/env python3

from __future__ import annotations

import json
import contextlib
import io
import subprocess
import tempfile
import unittest
from pathlib import Path

import matrix_report


class MatrixReportTest(unittest.TestCase):
    def test_selected_route_uses_final_route(self) -> None:
        log = "\n".join(
            (
                "Direct fallback: connection refused",
                "Route: relay, connections: 4",
            )
        )
        self.assertEqual(matrix_report.selected_route(log), "relay")
        self.assertEqual(
            matrix_report.selected_route("Route negotiation: WebRTC (forced)\n"),
            "webrtc",
        )

    def test_redacts_pairing_material_and_json_credentials(self) -> None:
        text = (
            "Code: K7M9Q2\n"
            "Link: https://kigo.example/#c=K7M9Q2\n"
            f"room={('ab' * 32)}\n"
        )
        redacted = matrix_report.redact_text(text, "K7M9Q2")
        self.assertNotIn("K7M9Q2", redacted)
        self.assertNotIn("ab" * 32, redacted)
        value = matrix_report.redact_value(
            {
                "credential": "secret-value",
                "credential_set": True,
                "nested": {"username": "temporary-user"},
            }
        )
        self.assertEqual(value["credential"], "[REDACTED]")
        self.assertTrue(value["credential_set"])
        self.assertEqual(value["nested"]["username"], "[REDACTED]")

    def test_scenario_fails_on_route_mismatch(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            sender = root / "sender.log"
            receiver = root / "receiver.log"
            output = root / "scenario.json"
            sender.write_text("Route: relay, connections: 4\n", encoding="utf-8")
            receiver.write_text("Route: relay, connections: 4\n", encoding="utf-8")
            args = matrix_report.parser().parse_args(
                [
                    "scenario",
                    "--name",
                    "test",
                    "--network-model",
                    "unit",
                    "--expected-route",
                    "direct",
                    "--input-sha256",
                    "same",
                    "--output-sha256",
                    "same",
                    "--sender-log",
                    str(sender),
                    "--receiver-log",
                    str(receiver),
                    "--output",
                    str(output),
                ]
            )
            self.assertEqual(args.func(args), 1)
            report = json.loads(output.read_text(encoding="utf-8"))
            self.assertEqual(report["status"], "failed")
            self.assertEqual(report["selected_route"], "relay")

    def test_public_validation_rejects_bad_configuration(self) -> None:
        args = matrix_report.parser().parse_args(
            [
                "validate-public",
                "--sender-host",
                "",
                "--receiver-host",
                "receiver.example",
                "--signal-url",
                "file:///tmp/kigo",
                "--relay",
                "relay.example:70000",
                "--payload-bytes",
                "0",
            ]
        )
        with contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(args.func(args), 2)

    def test_script_entrypoint_is_importable(self) -> None:
        result = subprocess.run(
            ["python3", str(Path(__file__).with_name("matrix_report.py")), "--help"],
            check=False,
            capture_output=True,
            text=True,
        )
        self.assertEqual(result.returncode, 0, result.stderr)


if __name__ == "__main__":
    unittest.main()
