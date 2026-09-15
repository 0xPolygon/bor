import json
import os
import pathlib
import secrets
import tempfile
import unittest
from unittest.mock import patch

import provision


class ProvisionTest(unittest.TestCase):
    def package(self, root, text=None):
        package = pathlib.Path(root) / "package"
        builder = package / "static_files/el/genesis/builder.sh"
        builder.parent.mkdir(parents=True)
        builder.write_text(text if text is not None else provision.GENESIS_MARKER + "\n")
        return package, builder

    def test_funder_key_stays_outside_the_package_and_logs(self):
        wallet = {"address": "0x" + "a" * 40, "private_key": "0x" + secrets.token_hex(32)}
        with tempfile.TemporaryDirectory() as root:
            package, builder = self.package(root)
            env_file = pathlib.Path(root) / "runner.env"
            with patch("provision.command", return_value=json.dumps([wallet])) as command, \
                    patch.dict(os.environ, {"GITHUB_ACTIONS": "false"}), patch("builtins.print") as output:
                provision.provision(package, env_file)
            command.assert_called_once_with("docker", "run", "--rm", "--entrypoint", "cast",
                                            provision.CAST_IMAGE, "wallet", "new", "--json")
            self.assertEqual(env_file.read_text(), f"REBROADCAST_FUNDER_KEY={wallet['private_key']}\n")
            self.assertEqual(env_file.stat().st_mode & 0o777, 0o600)
            self.assertNotIn(wallet["private_key"], builder.read_text())
            self.assertIn(wallet["address"][2:], builder.read_text())
            self.assertIn(provision.FUNDER_BALANCE, builder.read_text())
            self.assertNotIn(wallet["private_key"], str(output.call_args_list))

    def test_masks_key_before_exporting_in_ci(self):
        wallet = {"address": "0x" + "a" * 40, "private_key": "0x" + secrets.token_hex(32)}
        with tempfile.TemporaryDirectory() as root:
            package, builder = self.package(root)
            env_file = pathlib.Path(root) / "runner.env"
            with patch("provision.command", return_value=json.dumps({"success": True, "data": [wallet]})), \
                    patch.dict(os.environ, {"GITHUB_ACTIONS": "true"}), patch("builtins.print") as output:
                provision.provision(package, env_file)
            self.assertEqual(output.call_args_list[0].args, (f"::add-mask::{wallet['private_key']}",))

    def test_rejects_template_drift_and_duplicate_provisioning(self):
        for template in ("missing marker", provision.GENESIS_MARKER * 2,
                         "rebroadcast_funder\n" + provision.GENESIS_MARKER):
            with self.subTest(template=template), tempfile.TemporaryDirectory() as root:
                package, builder = self.package(root, template)
                with self.assertRaisesRegex(RuntimeError, "unmodified genesis"):
                    provision.prefund_genesis(package, "0x" + "a" * 40)
                self.assertEqual(builder.read_text(), template)

    def test_rejects_unsafe_wallet_values_before_export(self):
        for address, key in (("0x" + "a" * 40 + "\n", "0x" + secrets.token_hex(32)),
                             ("0x" + "a" * 40, "invalid\nINJECTED=value")):
            with self.subTest(address=address), tempfile.TemporaryDirectory() as root:
                package, builder = self.package(root)
                env_file = pathlib.Path(root) / "runner.env"
                wallet = {"address": address, "private_key": key}
                with patch("provision.command", return_value=json.dumps([wallet])), patch("builtins.print"):
                    with self.assertRaisesRegex(RuntimeError, "Invalid rebroadcast funder"):
                        provision.provision(package, env_file)
                self.assertFalse(env_file.exists())
                self.assertEqual(builder.read_text(), provision.GENESIS_MARKER + "\n")

    def test_refuses_to_write_the_key_into_the_package(self):
        with tempfile.TemporaryDirectory() as root:
            package, builder = self.package(root)
            with patch("provision.command") as command:
                with self.assertRaisesRegex(RuntimeError, "outside the Kurtosis package"):
                    provision.provision(package, package / "funder.env")
            command.assert_not_called()
            self.assertEqual(builder.read_text(), provision.GENESIS_MARKER + "\n")


if __name__ == "__main__":
    unittest.main()
