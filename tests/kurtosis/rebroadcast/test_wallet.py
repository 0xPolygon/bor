import json
import unittest

import e2e


class WalletTest(unittest.TestCase):
    def test_parses_legacy_and_enveloped_wallets(self):
        wallet = {"address": "fixture", "private_key": "fixture-key"}
        for payload in ([wallet], {"schema_version": 1, "success": True, "data": [wallet]}):
            with self.subTest(payload=payload):
                self.assertEqual(e2e.parse_wallet(json.dumps(payload)), wallet)

    def test_rejects_failed_or_malformed_wallet_results(self):
        for payload in (None, [], [{}, {}], [{}], [None],
                        [{"address": "fixture", "private_key": ""}],
                        {"success": False, "data": [{"address": "fixture", "private_key": "fixture-key"}]},
                        {"success": True, "data": []}, {"success": True}, {"data": []}):
            with self.subTest(payload=payload):
                with self.assertRaises(RuntimeError):
                    e2e.parse_wallet(json.dumps(payload))


if __name__ == "__main__":
    unittest.main()
