import itertools
import types
import unittest
from unittest.mock import Mock, patch

import e2e


class RecoveryBoundaryTest(unittest.TestCase):
    def fixture(self):
        test = e2e.Test.__new__(e2e.Test)
        test.args = types.SimpleNamespace(window=12, min_lag=3, timeout=240)
        test.summary = {}
        start = {"head": 396, "reference": 428, "lag": 32, "peers": 2,
                 "syncing": False, "identified": 19, "rebroadcast": 4}
        active = dict(start, reference=438, lag=42, peers=5,
                      syncing={"currentBlock": "0x18c"}, identified=24)
        recovered = dict(start, head=437, reference=439, lag=2, peers=5,
                         identified=25, rebroadcast=5)
        test.wait = Mock(return_value=start)
        return test, start, active, recovered

    def run_suppression(self, test, samples):
        test.sample = Mock(side_effect=samples)
        with patch("e2e.time.sleep"), patch("e2e.time.monotonic", side_effect=itertools.count()):
            test.suppression()

    def test_recovery_rebroadcast_is_outside_the_suppression_interval(self):
        for count in (4, 5):
            with self.subTest(rebroadcast=count):
                test, start, active, recovered = self.fixture()
                recovered["rebroadcast"] = count
                self.run_suppression(test, [active, recovered])
                evidence = test.summary["suppression"]
                self.assertEqual(evidence["start"], start)
                self.assertEqual(evidence["end"], active)
                self.assertEqual(evidence["catch_up_boundary"], recovered)
                self.assertEqual(evidence["identified_batches"], 5)
                self.assertEqual(evidence["rebroadcast_batches"], 0)

    def test_recovery_batches_cannot_supply_missing_suppression_evidence(self):
        test, start, active, recovered = self.fixture()
        active["identified"] = start["identified"] + 2
        with self.assertRaisesRegex(RuntimeError, "three stuck-tx batches"):
            self.run_suppression(test, [active, recovered])

    def test_rebroadcast_at_the_lag_threshold_still_fails(self):
        for syncing in (False, {"currentBlock": "0x18c"}):
            with self.subTest(syncing=syncing):
                test, start, active, recovered = self.fixture()
                behind = dict(recovered, lag=test.args.min_lag, syncing=syncing)
                with self.assertRaisesRegex(RuntimeError, "Out-of-sync node rebroadcast"):
                    self.run_suppression(test, [active, behind])

    def test_recovery_does_not_replace_active_sync_evidence(self):
        for syncing in (False, {"currentBlock": "0x1b5"}):
            with self.subTest(boundary_syncing=syncing):
                test, start, active, recovered = self.fixture()
                active["syncing"] = False
                recovered["syncing"] = syncing
                with self.assertRaisesRegex(RuntimeError, "never reported an active"):
                    self.run_suppression(test, [active, recovered])

    def test_recovery_does_not_replace_head_progress_or_connectivity(self):
        for changes, error in (({"head": 396}, "block did not advance"),
                               ({"peers": 0}, "connected-peer")):
            with self.subTest(changes=changes):
                test, start, active, recovered = self.fixture()
                recovered.update(changes)
                with self.assertRaisesRegex(RuntimeError, error):
                    self.run_suppression(test, [active, recovered])


if __name__ == "__main__":
    unittest.main()
