import json
import pathlib
import tempfile
import types
import unittest
from unittest.mock import Mock, call, patch

import e2e
import prepare


class ReliabilityTest(unittest.TestCase):
    def test_service_lookup_selects_requested_enclave_port(self):
        def container(port, enclave):
            return {"Name": "/l2-el-1-bor--" + enclave, "Id": enclave, "Image": "bor",
                    "NetworkSettings": {"Ports": {"8545/tcp": [{"HostPort": port}]}},
                    "Config": {"Labels": {"com.kurtosistech.private-ip": "172.16.0.2",
                                          "com.kurtosistech.enclave-id": enclave}}}
        inspected = [container("18545", "other"), container("28545", "requested")]
        with patch("e2e.command", side_effect=["http://127.0.0.1:28545", "other requested", json.dumps(inspected)]):
            self.assertEqual(e2e.service("requested", "l2-el-1-bor")["id"], "requested")

    def test_initialization_failure_writes_summary(self):
        with tempfile.TemporaryDirectory() as output:
            argv = ["e2e.py", "--enclave", "missing", "--artifacts", output]
            with patch("sys.argv", argv), patch("e2e.service", side_effect=RuntimeError("discovery failed")), patch("e2e.signal.signal"):
                with self.assertRaisesRegex(RuntimeError, "discovery failed"):
                    e2e.main()
            summary = json.loads((pathlib.Path(output) / "summary.json").read_text())
            self.assertEqual(summary["result"], "FAIL")
            self.assertEqual(summary["phase"], "initialization")
            self.assertIn("discovery failed", summary["error"])

    def test_partition_and_reconnect_cover_all_original_peers(self):
        test = e2e.Test.__new__(e2e.Test)
        test.target = {"url": "target", "ip": "172.16.0.9"}
        test.nodes = [{"id": "validator1"}, {"id": "validator2"}, {"id": "rpc"}]
        test.args = types.SimpleNamespace(initial_gap=20)
        test.shaped = []
        test.tc = Mock()
        test.set_peer = Mock()
        test.wait = Mock()
        enodes = ["enode://validator1", "enode://validator2", "enode://rpc"]
        with patch("e2e.rpc", return_value=[{"enode": node} for node in enodes]):
            test.partition()
        self.assertEqual(test.shaped, test.nodes)
        test.create_gap()
        self.assertEqual(test.set_peer.call_args_list, [call("remove", node) for node in enodes])
        test.set_peer.reset_mock()
        test.reconnect()
        self.assertEqual(test.set_peer.call_args_list, [call("add", node) for node in enodes])
        self.assertEqual(test.removed_peers, [])

    def test_reconnect_continues_after_failure(self):
        test = e2e.Test.__new__(e2e.Test)
        test.removed_peers = ["first", "second"]
        test.set_peer = Mock(side_effect=[RuntimeError("first failed"), None])
        with self.assertRaisesRegex(RuntimeError, "Peer cleanup failed"):
            test.reconnect()
        self.assertEqual(test.set_peer.call_args_list, [call("add", "first"), call("add", "second")])
        self.assertEqual(test.removed_peers, ["first"])

    def test_fixture_uses_new_account_and_nonce_zero(self):
        test = e2e.Test.__new__(e2e.Test)
        test.args = types.SimpleNamespace(funding_key="funder")
        test.target = {"url": "http://127.0.0.1:8545"}
        test.nodes = []
        test.summary = {}
        test.wait = Mock()
        test.cast = Mock(side_effect=[json.dumps([{"address": "fresh", "private_key": "fresh-key"}]), "funding", "fixture"])
        with patch("e2e.rpc", return_value={"status": "0x1"}):
            test.fund_fixture()
        test.seed()
        funding_args, fixture_args = test.cast.call_args_list[1].args, test.cast.call_args_list[2].args
        self.assertIn("funder", funding_args)
        self.assertIn("fresh", funding_args)
        self.assertIn("fresh-key", fixture_args)
        self.assertNotIn("funder", fixture_args)
        self.assertEqual(fixture_args[fixture_args.index("--nonce") + 1], "0")
        self.assertEqual(test.tx, "fixture")

    def test_sample_rejects_batches_from_a_contaminated_pool(self):
        for hashes in ({"fixture"}, {"fixture", "other"}, set()):
            with self.subTest(hashes=hashes), tempfile.TemporaryDirectory() as output:
                test = e2e.Test.__new__(e2e.Test)
                test.target = {"url": "target"}
                test.producers = [{"url": "producer"}]
                test.output = pathlib.Path(output)
                test.tx = "fixture"
                test.logs = Mock(return_value="Rebroadcast stuck transactions\n" * 3)
                def rpc(url, method, params=None):
                    return {"eth_blockNumber": "0x20", "net_peerCount": "0x1",
                            "eth_syncing": False, "eth_getTransactionByHash": {"blockHash": None}}[method]
                with patch("e2e.rpc", side_effect=rpc), patch("e2e.pool_hashes", return_value=hashes), patch("builtins.print"):
                    if hashes == {"fixture"}:
                        self.assertEqual(test.sample("baseline")["rebroadcast"], 3)
                    else:
                        with self.assertRaisesRegex(RuntimeError, "only transaction"):
                            test.sample("baseline")

    def test_pool_hashes_includes_queued_transactions(self):
        content = {"pending": {"a": {"0": {"hash": "fixture"}}},
                   "queued": {"b": {"2": {"hash": "other"}}}}
        with patch("e2e.rpc", return_value=content):
            self.assertEqual(e2e.pool_hashes("target"), {"fixture", "other"})

    def test_prepare_template_and_reject_drift(self):
        original = 'cache = 4096\nverbosity = {{.log_level_to_int}}\nrebroadcast-interval = "10s"\nrebroadcast-max-age = "1m"\n'
        with tempfile.TemporaryDirectory() as package:
            config = pathlib.Path(package) / "static_files/el/bor/config.toml"
            config.parent.mkdir(parents=True)
            config.write_text(original)
            prepare.prepare(package)
            updated = config.read_text()
            self.assertIn('verbosity = 4', updated)
            self.assertIn('rebroadcast-interval = "2s"', updated)
            with self.assertRaisesRegex(RuntimeError, "Expected one fixture setting"):
                prepare.prepare(package)
            self.assertEqual(config.read_text(), updated)


if __name__ == "__main__":
    unittest.main()
