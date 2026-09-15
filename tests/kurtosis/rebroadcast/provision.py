#!/usr/bin/env python3
"""Provision a per-run rebroadcast funder in a disposable Kurtosis package."""

import argparse
import os
import pathlib
import re

from e2e import CAST_IMAGE, command, parse_wallet


GENESIS_MARKER = "# Add the alloc field to the temporary EL genesis to create the final EL genesis."
FUNDER_BALANCE = "0x8ac7230489e80000"  # 10 native tokens, only on the disposable devnet.


def prefund_genesis(package, address):
    if re.fullmatch(r"0x[0-9a-fA-F]{40}", address) is None:
        raise RuntimeError("Invalid rebroadcast funder address")
    builder = pathlib.Path(package) / "static_files/el/genesis/builder.sh"
    text = builder.read_text()
    if text.count(GENESIS_MARKER) != 1 or "rebroadcast_funder" in text:
        raise RuntimeError("Expected one unmodified genesis allocation merge")
    allocation = (
        f'rebroadcast_funder="{address[2:].lower()}"\n'
        f'jq --arg a "$rebroadcast_funder" --arg b "{FUNDER_BALANCE}" '
        "'.alloc[$a] = {\"balance\": $b}' \"${EL_GENESIS_ALLOC_FILE}\" > tmp.json\n"
        'mv tmp.json "${EL_GENESIS_ALLOC_FILE}"\n\n'
    )
    builder.write_text(text.replace(GENESIS_MARKER, allocation + GENESIS_MARKER))


def provision(package, env_file):
    if pathlib.Path(env_file).resolve().is_relative_to(pathlib.Path(package).resolve()):
        raise RuntimeError("Keep the funder environment file outside the Kurtosis package")
    wallet = parse_wallet(command("docker", "run", "--rm", "--entrypoint", "cast",
                                  CAST_IMAGE, "wallet", "new", "--json"))
    key = wallet["private_key"]
    if re.fullmatch(r"0x[0-9a-fA-F]{64}", key) is None:
        raise RuntimeError("Invalid rebroadcast funder key")
    if os.environ.get("GITHUB_ACTIONS") == "true":
        print(f"::add-mask::{key}", flush=True)
    prefund_genesis(package, wallet["address"])
    # Keep the key out of the Kurtosis package and uploaded diagnostics.
    descriptor = os.open(env_file, os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o600)
    with os.fdopen(descriptor, "a") as output:
        os.fchmod(output.fileno(), 0o600)
        output.write(f"REBROADCAST_FUNDER_KEY={key}\n")
    print(f"Provisioned dedicated rebroadcast funder {wallet['address']}")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("package")
    parser.add_argument("--env-file", required=True,
                        help="Runner environment file outside the package and diagnostics directory")
    args = parser.parse_args()
    provision(args.package, args.env_file)
