package cli

import (
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/internal/cli/flagset"
	"github.com/ethereum/go-ethereum/node"
)

type PurgeWhitelistedEntriesCommand struct {
	*Meta
}

func (c *PurgeWhitelistedEntriesCommand) MarkDown() string {
	items := []string{
		"# Purge whitelisted entries",
		"The ```bor purge-whitelisted-entries``` command will remove locally whitelisted checkpoint and milestone entries from the database.",
		c.Flags().MarkDown(),
	}

	return strings.Join(items, "\n\n")
}

// Help implements the cli.Command interface
func (c *PurgeWhitelistedEntriesCommand) Help() string {
	return `Usage: bor purge-whitelisted-entries

  This command is used to purge the locally whitelisted checkpoint and milestone entries from the database.`
}

func (c *PurgeWhitelistedEntriesCommand) Flags() *flagset.Flagset {
	flags := c.NewFlagSet("purge-whitelisted-entries")
	return flags
}

// Synopsis implements the cli.Command interface
func (c *PurgeWhitelistedEntriesCommand) Synopsis() string {
	return "Purge the locally whitelisted checkpoint and milestone entries"
}

// Run implements the cli.Command interface
func (c *PurgeWhitelistedEntriesCommand) Run(args []string) int {
	flags := c.Flags()

	if err := flags.Parse(args); err != nil {
		c.UI.Error(err.Error())
		return 1
	}

	datadir := c.dataDir
	if datadir == "" {
		c.UI.Error("--datadir flag is required")
		return 1
	}

	c.UI.Output("Starting to prune whitelisted entries. Opening database at path: " + datadir)

	// Open the underlying key value database
	node, err := node.New(&node.Config{
		DataDir: datadir,
	})
	if err != nil {
		c.UI.Error(fmt.Sprintf("Failed to create node: %v", err))
		return 1
	}
	chaindb, err := node.OpenDatabase(datadir+"/bor/chaindata", 1024, 2000, "", false)
	if err != nil {
		c.UI.Error(fmt.Sprintf("Failed to open underlying key value database: %v", err))
		return 1
	}
	c.UI.Output("Opened database at path: " + datadir)

	// Purge all whitelisted entries from db
	rawdb.PurgeWhitelistedEntriesFromDb(chaindb)
	c.UI.Output("Finished purge attempt for local whitelisted entries")

	chaindb.Close()
	return 0
}
