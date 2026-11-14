# db pebble-upgrade

The ```bor db pebble-upgrade``` command upgrades the chain database at the given datadir from the legacy pebble v1 format to `FormatFlushableIngest`, the oldest format pebble v2 can open. Bor opens a database in that format with pebble v2 from then on. Older bor binaries, which use pebble v1, can still open the upgraded database. The node must be stopped first: the command fails while the database is locked.

## Options

- ```datadir```: Path of the data directory to store information