import json
import pathlib
import secrets
import subprocess
import tempfile
import traceback
import unittest
from unittest.mock import Mock, patch

import e2e


class CommandErrorTest(unittest.TestCase):
    def test_timeout_does_not_expose_signing_key_in_summary_or_traceback(self):
        key = "0x" + secrets.token_hex(32)
        args = ("docker", "run", "cast", "send", "--private-key", key)
        failure = subprocess.TimeoutExpired(args, 60, output=key.encode(), stderr=key.encode())
        with tempfile.TemporaryDirectory() as output:
            test = Mock(summary={})
            test.run.side_effect = lambda: e2e.command(*args)
            argv = ["e2e.py", "--enclave", "test", "--artifacts", output]
            with patch("e2e.Test", return_value=test), patch("sys.argv", argv), \
                    patch("e2e.signal.signal"), patch("e2e.subprocess.run", side_effect=failure):
                try:
                    e2e.main()
                except Exception as error:
                    rendered = "".join(traceback.format_exception(type(error), error, error.__traceback__))
                    self.assertIsInstance(error, RuntimeError)
                    self.assertIn("docker timed out after 60 seconds", str(error))
                    self.assertNotIn(key, rendered)
                else:
                    self.fail("timeout must fail the test")
            test.cleanup.assert_called_once_with()
            raw = (pathlib.Path(output) / "summary.json").read_text()
            self.assertNotIn(key, raw)
            self.assertEqual(json.loads(raw)["result"], "FAIL")

    def test_exit_error_redacts_signing_key_from_either_stream(self):
        key = "0x" + secrets.token_hex(32)
        for stream in ("stdout", "stderr"):
            with self.subTest(stream=stream):
                result = subprocess.CompletedProcess([], 1, "", "")
                setattr(result, stream, f"invalid signing credential: {key}")
                with patch("e2e.subprocess.run", return_value=result):
                    with self.assertRaises(RuntimeError) as raised:
                        e2e.command("docker", "run", "cast", "send", "--private-key", key)
                self.assertNotIn(key, str(raised.exception))
                self.assertIn("invalid signing credential: [REDACTED]", str(raised.exception))

    def test_successful_output_is_unchanged(self):
        result = subprocess.CompletedProcess([], 0, "transaction hash", "")
        with patch("e2e.subprocess.run", return_value=result):
            self.assertEqual(e2e.command("docker", "run", "cast"), "transaction hash")


if __name__ == "__main__":
    unittest.main()
