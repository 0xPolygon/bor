#!/usr/bin/env python3
"""Configure the Kurtosis Bor template for rebroadcast assertions."""

import pathlib
import sys


def prepare(package):
    config = pathlib.Path(package) / "static_files/el/bor/config.toml"
    text = config.read_text()
    for old, new in {
        'verbosity = {{.log_level_to_int}}': 'verbosity = 4',
        'rebroadcast-interval = "10s"': 'rebroadcast-interval = "2s"',
        'rebroadcast-max-age = "1m"': 'rebroadcast-max-age = "30m"',
    }.items():
        if text.count(old) != 1:
            raise RuntimeError(f"Expected one fixture setting: {old}")
        text = text.replace(old, new)
    config.write_text(text)


if __name__ == "__main__":
    prepare(sys.argv[1])
