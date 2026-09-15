import types
import unittest
from unittest.mock import Mock, patch

import e2e


class SyncTimingTest(unittest.TestCase):
    def fixture(self):
        test = e2e.Test.__new__(e2e.Test)
        test.args = types.SimpleNamespace(window=12, min_lag=3, timeout=60)
        test.summary = {}
        start = {"head": 412, "lag": 31, "peers": 1, "syncing": False,
                 "identified": 14, "rebroadcast": 13}
        test.wait = Mock(return_value=start)
        return test, start

    def test_ancestor_discovery_does_not_consume_active_sync_window(self):
        test, start = self.fixture()
        active = dict(start, syncing={"currentBlock": "0x19c"}, identified=19)
        advanced = dict(active, head=420, identified=20)
        test.sample = Mock(side_effect=[start, dict(start, identified=17), active, advanced])
        with patch("e2e.time.sleep"), patch("e2e.time.monotonic", side_effect=[0, 0, 13, 20, 20, 28]):
            test.suppression()
        evidence = test.summary["suppression"]
        self.assertEqual(evidence["start"], start)
        self.assertEqual(evidence["active_sync"], active)
        self.assertEqual(evidence["end"], advanced)
        self.assertEqual(evidence["identified_batches"], 6)
        self.assertEqual(evidence["rebroadcast_batches"], 0)

    def test_preparation_still_checks_rebroadcast_and_connectivity(self):
        for changes, error in [({"rebroadcast": 14}, "rebroadcast"),
                               ({"peers": 0}, "connected-peer")]:
            with self.subTest(changes=changes):
                test, start = self.fixture()
                test.sample = Mock(return_value=dict(start, **changes))
                with patch("e2e.time.monotonic", side_effect=[0, 13]):
                    with self.assertRaisesRegex(RuntimeError, error):
                        test.suppression()

    def test_initial_sample_can_report_active_sync(self):
        test, start = self.fixture()
        start["syncing"] = {"currentBlock": "0x19c"}
        test.sample = Mock(return_value=dict(start, head=420, identified=17, syncing=False))
        with patch("e2e.time.monotonic", side_effect=[0, 0]):
            test.suppression()
        self.assertEqual(test.summary["suppression"]["active_sync"], start)

    def test_active_sync_without_head_progress_still_times_out(self):
        test, start = self.fixture()
        test.sample = Mock(return_value=dict(start, syncing={"currentBlock": "0x19c"}, identified=17))
        with patch("e2e.time.sleep"), patch("e2e.time.monotonic", side_effect=[0, 20, 20, 32]):
            with self.assertRaisesRegex(RuntimeError, "block did not advance"):
                test.suppression()

    def test_preparation_has_a_bounded_timeout(self):
        test, start = self.fixture()
        test.sample = Mock(return_value=dict(start, head=413, identified=17))
        with patch("e2e.time.sleep"), patch("e2e.time.monotonic", side_effect=[0, 0, 60]):
            with self.assertRaisesRegex(RuntimeError, "never reported an active"):
                test.suppression()

    def test_repeated_sync_reports_do_not_extend_the_window(self):
        test, start = self.fixture()
        test.sample = Mock(return_value=dict(start, syncing={"currentBlock": "0x19c"}, identified=17))
        with patch("e2e.time.sleep"), patch("e2e.time.monotonic", side_effect=[0, 0, 0, 11, 12]):
            with self.assertRaisesRegex(RuntimeError, "block did not advance"):
                test.suppression()
        self.assertEqual(test.sample.call_count, 2)

    def test_final_import_can_complete_catchup_after_suppression(self):
        test, start = self.fixture()
        active = dict(start, identified=17, syncing={"currentBlock": "0x19c"})
        test.sample = Mock(side_effect=[active, dict(start, head=450, lag=0, identified=17)])
        with patch("e2e.time.sleep"), patch("e2e.time.monotonic", side_effect=[0, 0, 0, 1]):
            test.suppression()
        self.assertEqual(test.summary["suppression"]["end"]["head"], 450)


if __name__ == "__main__":
    unittest.main()
