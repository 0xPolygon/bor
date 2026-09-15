import json
import pathlib
import subprocess
import tempfile
import types
import unittest
from unittest.mock import Mock, call, patch

import e2e


class LogReadTest(unittest.TestCase):
    def fixture(self, output):
        test = e2e.Test.__new__(e2e.Test)
        test.output = pathlib.Path(output)
        test.target = {"id": "target-container", "url": "target"}
        test.producers = [{"url": "producer"}]
        test.start = "2026-09-16T00:00:00+00:00"
        test.tx = None
        return test

    def failures(self):
        return [subprocess.CalledProcessError(1, ["docker", "logs"],
                                             output="partial logs", stderr="temporary log error"),
                subprocess.TimeoutExpired(["docker", "logs"], 5,
                                          output=b"partial logs", stderr=b"temporary log error")]

    def test_success_preserves_both_streams_without_retry(self):
        with tempfile.TemporaryDirectory() as output:
            test = self.fixture(output)
            result = subprocess.CompletedProcess([], 0, "stdout\n", "stderr\n")
            with patch("e2e.subprocess.run", return_value=result) as run, patch("e2e.time.sleep") as sleep:
                self.assertEqual(test.logs(), "stdout\nstderr\n")
            run.assert_called_once_with(["docker", "logs", "--since", test.start, "target-container"],
                                        capture_output=True, text=True, timeout=5, check=True)
            sleep.assert_not_called()
            self.assertEqual((test.output / "target.log").read_text(), "stdout\nstderr\n")
            self.assertFalse((test.output / "log-read-errors.jsonl").exists())

    def test_retries_exit_errors_and_timeouts_without_counting_partial_logs(self):
        for failure in self.failures():
            with self.subTest(failure=type(failure).__name__), tempfile.TemporaryDirectory() as output:
                test = self.fixture(output)
                result = subprocess.CompletedProcess([], 0, "complete stdout\n", "complete stderr\n")
                with patch("e2e.subprocess.run", side_effect=[failure, failure, result]) as run, \
                        patch("e2e.time.sleep") as sleep:
                    self.assertEqual(test.logs(), result.stdout + result.stderr)
                self.assertEqual(run.call_count, 3)
                self.assertTrue(all(c == run.call_args_list[0] for c in run.call_args_list))
                self.assertEqual(sleep.call_args_list, [call(1), call(1)])
                self.assertEqual((test.output / "target.log").read_text(), result.stdout + result.stderr)
                errors = [json.loads(line) for line in (test.output / "log-read-errors.jsonl").read_text().splitlines()]
                self.assertEqual([error["attempt"] for error in errors], [1, 2])
                self.assertTrue(all("temporary log error" in error["detail"] for error in errors))

    def test_exhaustion_preserves_last_complete_log_and_fails_the_phase(self):
        for failure in self.failures():
            with self.subTest(failure=type(failure).__name__), tempfile.TemporaryDirectory() as output:
                test = self.fixture(output)
                (test.output / "target.log").write_text("last complete log")
                test.args = types.SimpleNamespace(timeout=240)
                with patch("e2e.subprocess.run", side_effect=failure) as run, \
                        patch("e2e.time.sleep") as sleep, patch("e2e.rpc", return_value="0x20"):
                    with self.assertRaisesRegex(RuntimeError, "Docker logs failed after 3 attempts.*temporary log error"):
                        test.wait("baseline", lambda sample: True)
                self.assertEqual(run.call_count, 3)
                self.assertEqual(sleep.call_args_list, [call(1), call(1)])
                self.assertEqual((test.output / "target.log").read_text(), "last complete log")
                self.assertFalse((test.output / "samples.jsonl").exists())
                self.assertEqual(len((test.output / "log-read-errors.jsonl").read_text().splitlines()), 3)

    def test_log_retry_does_not_hide_rebroadcast_during_suppression(self):
        with tempfile.TemporaryDirectory() as output:
            test = self.fixture(output)
            test.args = types.SimpleNamespace(timeout=60, window=12, min_lag=3)
            test.summary = {}
            test.wait = Mock(return_value={"head": 20, "lag": 10, "peers": 1, "syncing": False,
                                          "identified": 0, "rebroadcast": 0})
            result = subprocess.CompletedProcess([], 0, "Rebroadcast stuck transactions\n", "")
            def rpc(url, method, params=None):
                return {"eth_blockNumber": "0x14" if url == "target" else "0x20",
                        "net_peerCount": "0x1", "eth_syncing": False}[method]
            with patch("e2e.subprocess.run", side_effect=[self.failures()[0], result]), \
                    patch("e2e.time.sleep"), patch("e2e.rpc", side_effect=rpc), patch("builtins.print"):
                with self.assertRaisesRegex(RuntimeError, "Out-of-sync node rebroadcast"):
                    test.suppression()

    def test_interruption_is_not_retried(self):
        with tempfile.TemporaryDirectory() as output:
            test = self.fixture(output)
            with patch("e2e.subprocess.run", side_effect=KeyboardInterrupt) as run, \
                    patch("e2e.time.sleep") as sleep:
                with self.assertRaises(KeyboardInterrupt):
                    test.logs()
            self.assertEqual(run.call_count, 1)
            sleep.assert_not_called()


if __name__ == "__main__":
    unittest.main()
