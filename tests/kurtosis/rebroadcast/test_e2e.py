import json
import pathlib
import tempfile
import types
import unittest
from unittest.mock import Mock, patch

import e2e


class SuppressionTest(unittest.TestCase):
    def test_service_matches_kurtosis_container_name(self):
        inspected = [{
            "Name": "/l2-el-4-bor-heimdall-v2-rpc--service-uuid",
            "NetworkSettings": {"Ports": {"8545/tcp": [{"HostPort": "18545"}]}},
            "Config": {"Labels": {
                "com.kurtosistech.private-ip": "172.16.0.10",
                "com.kurtosistech.enclave-id": "enclave-uuid",
            }},
            "Id": "target-id", "Image": "bor:local",
        }]
        with patch("e2e.command", side_effect=["http://127.0.0.1:18545\n", "target-id\n", json.dumps(inspected)]):
            target = e2e.service("rebroadcast", "l2-el-4-bor-heimdall-v2-rpc")
        self.assertEqual(target["id"], "target-id")

    def test_requires_active_pool_and_connected_lagging_node(self):
        start = {"head": 20, "lag": 10, "peers": 3, "syncing": False,
                 "identified": 4, "rebroadcast": 3}
        for name, changes, error in [
            ("suppressed", {"head": 21, "syncing": {"currentBlock": "0x14"},
                            "identified": 7}, None),
            ("rebroadcast while behind", {"head": 21, "identified": 7, "rebroadcast": 4}, "rebroadcast"),
            ("idle pool", {}, "three stuck-tx batches"),
            ("disconnected", {"peers": 0}, "precondition"),
            ("caught up before evidence", {"head": 21, "lag": 0}, "three stuck-tx batches"),
            ("head stalled", {"identified": 7}, "block did not advance"),
            ("no active sync", {"head": 21, "identified": 7}, "never reported"),
        ]:
            with self.subTest(name=name):
                test = e2e.Test.__new__(e2e.Test)
                test.args = types.SimpleNamespace(window=8, min_lag=3, timeout=20)
                test.summary = {}
                test.wait = Mock(return_value=start)
                test.sample = Mock(return_value=dict(start, **changes))
                with patch("e2e.time.sleep"), patch("e2e.time.monotonic", side_effect=[0, 0, 20, 21]):
                    if error:
                        with self.assertRaisesRegex(RuntimeError, error):
                            test.suppression()
                    else:
                        test.suppression()
                        self.assertEqual(test.summary["suppression"]["identified_batches"], 3)

    def test_cleanup_continues_after_one_failure(self):
        test = e2e.Test.__new__(e2e.Test)
        first, second = {"id": "first"}, {"id": "second"}
        test.shaped = [first, second]
        test.tc = Mock(side_effect=[RuntimeError("cleanup failed"), ""])
        with self.assertRaisesRegex(RuntimeError, "Network cleanup failed"):
            test.restore()
        self.assertEqual(test.tc.call_count, 2)
        self.assertEqual(test.shaped, [first])

    def test_cleanup_attempts_every_action(self):
        actions = ("restore", "reconnect", "restore_fees")
        for failing in ((), ("restore",), ("reconnect",), ("restore_fees",), actions):
            with self.subTest(failing=failing):
                test = e2e.Test.__new__(e2e.Test)
                for name in actions:
                    setattr(test, name, Mock(side_effect=RuntimeError(name) if name in failing else None))
                if failing:
                    with self.assertRaises(RuntimeError) as raised:
                        test.cleanup()
                    for name in failing:
                        self.assertIn(name, str(raised.exception))
                else:
                    test.cleanup()
                for name in actions:
                    getattr(test, name).assert_called_once_with()

    def test_fee_cleanup_continues_after_one_failure(self):
        test = e2e.Test.__new__(e2e.Test)
        first, second = {"id": "first"}, {"id": "second"}
        test.fees = [(first, 30), (second, 40)]
        test.gas_price = Mock(side_effect=[RuntimeError("first failed"), None])
        with self.assertRaisesRegex(RuntimeError, "Gas-price cleanup failed"):
            test.restore_fees()
        self.assertEqual(test.gas_price.call_count, 2)
        self.assertEqual(test.fees, [(first, 30)])

    def test_main_records_cleanup_failure_after_all_restorations(self):
        with tempfile.TemporaryDirectory() as output:
            test = e2e.Test.__new__(e2e.Test)
            test.output = pathlib.Path(output)
            test.summary = {}
            test.run = Mock()
            test.restore = Mock(side_effect=RuntimeError("network rule remains"))
            test.reconnect = Mock()
            test.restore_fees = Mock(side_effect=RuntimeError("fee remains"))
            argv = ["e2e.py", "--enclave", "test", "--artifacts", output]
            with patch("e2e.Test", return_value=test), patch("sys.argv", argv), patch("e2e.signal.signal"):
                with self.assertRaisesRegex(RuntimeError, "Cleanup failed"):
                    e2e.main()
            test.reconnect.assert_called_once_with()
            test.restore_fees.assert_called_once_with()
            summary = json.loads((test.output / "summary.json").read_text())
            self.assertEqual(summary["result"], "FAIL")
            self.assertIn("network rule remains", summary["cleanup_error"])
            self.assertIn("fee remains", summary["cleanup_error"])


if __name__ == "__main__":
    unittest.main()
