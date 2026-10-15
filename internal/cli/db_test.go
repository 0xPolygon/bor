package cli

import (
	"path/filepath"
	"testing"

	pebblev1 "github.com/cockroachdb/pebble"
	"github.com/mitchellh/cli"
	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/ethdb/leveldb"
	"github.com/ethereum/go-ethereum/ethdb/pebble"
)

// newLegacyChaindata creates <datadir>/bor/chaindata in the format bor has
// always written: pebble v1 with no FormatMajorVersion set.
func newLegacyChaindata(t *testing.T) (string, string) {
	t.Helper()

	datadir := t.TempDir()
	path := filepath.Join(datadir, "bor", chaindataPath)

	db, err := pebblev1.Open(path, &pebblev1.Options{})
	require.NoError(t, err)
	require.Equal(t, pebblev1.FormatMostCompatible, db.FormatMajorVersion())
	require.NoError(t, db.Set([]byte("foo"), []byte("bar"), pebblev1.Sync))
	require.NoError(t, db.Close())

	return datadir, path
}

func runPebbleUpgrade(t *testing.T, datadir string) (int, *cli.MockUi) {
	t.Helper()

	ui := cli.NewMockUi()
	cmd := &PebbleUpgradeCommand{UI: ui}

	return cmd.Run([]string{"--datadir", datadir}), ui
}

func TestDBCommandsRegistered(t *testing.T) {
	t.Parallel()

	commands := Commands()
	for name, want := range map[string]MarkDownCommand{
		"db":                &DBCommand{},
		"db pebble-upgrade": &PebbleUpgradeCommand{},
	} {
		factory, ok := commands[name]
		require.True(t, ok, name)

		cmd, err := factory()
		require.NoError(t, err)
		require.IsType(t, want, cmd)
		require.NotEmpty(t, cmd.Help())
		require.NotEmpty(t, cmd.Synopsis())
		require.NotEmpty(t, cmd.MarkDown())
	}

	require.Equal(t, cli.RunResultHelp, (&DBCommand{}).Run(nil))
}

func TestPebbleUpgradeCommand(t *testing.T) {
	t.Parallel()

	t.Run("upgrades a legacy v1 database", func(t *testing.T) {
		t.Parallel()

		datadir, path := newLegacyChaindata(t)
		require.True(t, pebble.NeedsV1(path))

		code, ui := runPebbleUpgrade(t, datadir)
		require.Equal(t, 0, code, ui.ErrorWriter.String())
		require.False(t, pebble.NeedsV1(path))

		_, ver, err := pebble.PeekFormatVersion(path)
		require.NoError(t, err)
		require.Equal(t, uint64(pebblev1.FormatFlushableIngest), ver)

		// Older bor binaries use pebble v1 and must still open the result.
		db, err := pebblev1.Open(path, &pebblev1.Options{})
		require.NoError(t, err)
		value, closer, err := db.Get([]byte("foo"))
		require.NoError(t, err)
		require.Equal(t, []byte("bar"), value)
		require.NoError(t, closer.Close())
		require.NoError(t, db.Close())

		code, ui = runPebbleUpgrade(t, datadir)
		require.Equal(t, 0, code, ui.ErrorWriter.String())
	})

	t.Run("refuses a database in use", func(t *testing.T) {
		t.Parallel()

		datadir, path := newLegacyChaindata(t)
		db, err := pebblev1.Open(path, &pebblev1.Options{})
		require.NoError(t, err)
		defer db.Close()

		code, ui := runPebbleUpgrade(t, datadir)
		require.Equal(t, 1, code)
		require.NotEmpty(t, ui.ErrorWriter.String())
		require.True(t, pebble.NeedsV1(path))
	})

	t.Run("refuses a leveldb database", func(t *testing.T) {
		t.Parallel()

		datadir := t.TempDir()
		db, err := leveldb.New(filepath.Join(datadir, "bor", chaindataPath), 16, 16, "", false)
		require.NoError(t, err)
		require.NoError(t, db.Close())

		code, ui := runPebbleUpgrade(t, datadir)
		require.Equal(t, 1, code)
		require.Contains(t, ui.ErrorWriter.String(), "not pebble")
	})

	t.Run("rejects an unknown flag", func(t *testing.T) {
		t.Parallel()

		ui := cli.NewMockUi()
		code := (&PebbleUpgradeCommand{UI: ui}).Run([]string{"--unknown"})
		require.Equal(t, 1, code)
		require.NotEmpty(t, ui.ErrorWriter.String())
	})

	t.Run("refuses a missing database", func(t *testing.T) {
		t.Parallel()

		code, ui := runPebbleUpgrade(t, t.TempDir())
		require.Equal(t, 1, code)
		require.Contains(t, ui.ErrorWriter.String(), "no database found")
	})
}
