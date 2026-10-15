package cli

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/ethdb/pebble"
	"github.com/ethereum/go-ethereum/internal/cli/flagset"
	"github.com/ethereum/go-ethereum/internal/cli/server"
	"github.com/ethereum/go-ethereum/log"

	"github.com/mitchellh/cli"
)

// DBCommand is the command to group the database commands
type DBCommand struct {
	UI cli.Ui
}

// MarkDown implements cli.MarkDown interface
func (c *DBCommand) MarkDown() string {
	items := []string{
		"# db",
		"The ```db``` command groups database related actions:",
		"- [```db pebble-upgrade```](./db_pebble-upgrade.md): Upgrade a pebble v1 database to a format pebble v2 can open.",
	}

	return strings.Join(items, "\n\n")
}

// Help implements the cli.Command interface
func (c *DBCommand) Help() string {
	return `Usage: bor db <subcommand>

  This command groups database related actions.

  Upgrade a pebble v1 database so that pebble v2 can open it:

    $ bor db pebble-upgrade`
}

// Synopsis implements the cli.Command interface
func (c *DBCommand) Synopsis() string {
	return "Database related commands"
}

// Run implements the cli.Command interface
func (c *DBCommand) Run(args []string) int {
	return cli.RunResultHelp
}

// PebbleUpgradeCommand ratchets an existing pebble v1 chain database to the
// oldest format pebble v2 supports.
type PebbleUpgradeCommand struct {
	UI cli.Ui

	datadir string
}

// MarkDown implements cli.MarkDown interface
func (c *PebbleUpgradeCommand) MarkDown() string {
	items := []string{
		"# db pebble-upgrade",
		"The ```bor db pebble-upgrade``` command upgrades the chain database at the given datadir from the legacy pebble v1 format to `FormatFlushableIngest`, the oldest format pebble v2 can open. Bor opens a database in that format with pebble v2 from then on. Older bor binaries, which use pebble v1, can still open the upgraded database. The node must be stopped first: the command fails while the database is locked.",
		c.Flags().MarkDown(),
	}

	return strings.Join(items, "\n\n")
}

// Help implements the cli.Command interface
func (c *PebbleUpgradeCommand) Help() string {
	return `Usage: bor db pebble-upgrade --datadir <datadir>

  This command upgrades a pebble v1 chain database so that pebble v2 can open it.
  Stop the node before running it.` + c.Flags().Help()
}

// Synopsis implements the cli.Command interface
func (c *PebbleUpgradeCommand) Synopsis() string {
	return "Upgrade a pebble v1 database to a format pebble v2 can open"
}

// Flags: datadir
func (c *PebbleUpgradeCommand) Flags() *flagset.Flagset {
	flags := flagset.NewFlagSet("pebble-upgrade")

	flags.StringFlag(&flagset.StringFlag{
		Name:  "datadir",
		Value: &c.datadir,
		Usage: "Path of the data directory to store information",
	})

	return flags
}

// Run implements the cli.Command interface
func (c *PebbleUpgradeCommand) Run(args []string) int {
	flags := c.Flags()

	if err := flags.Parse(args); err != nil {
		c.UI.Error(err.Error())
		return 1
	}

	datadir := c.datadir
	if datadir == "" {
		datadir = server.DefaultDataDir()
	}

	if err := upgradePebbleChaindata(filepath.Join(datadir, "bor", chaindataPath)); err != nil {
		c.UI.Error(err.Error())
		return 1
	}

	return 0
}

func upgradePebbleChaindata(path string) error {
	if !common.FileExist(path) {
		return fmt.Errorf("no database found at %s", path)
	}

	if dbType := rawdb.PreexistingDatabase(path); dbType != rawdb.DBPebble {
		return fmt.Errorf("database at %s is %q, not pebble", path, dbType)
	}

	_, before, err := pebble.PeekFormatVersion(path)
	if err != nil {
		return fmt.Errorf("failed to read the database format at %s: %w", path, err)
	}

	log.Info("Upgrading pebble database", "path", path, "format", before)

	start := time.Now()
	if err := pebble.Upgrade(path); err != nil {
		return err
	}

	_, after, err := pebble.PeekFormatVersion(path)
	if err != nil {
		return fmt.Errorf("failed to read the database format at %s: %w", path, err)
	}

	log.Info("Pebble database upgraded", "path", path, "from", before, "to", after, "elapsed", common.PrettyDuration(time.Since(start)))

	return nil
}
