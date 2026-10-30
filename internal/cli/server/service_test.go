package server

import (
	"context"
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/internal/cli/server/proto"
)

func TestGatherBlocks(t *testing.T) {
	type c struct {
		ABlock *big.Int
		BBlock *big.Int
	}

	type d struct {
		DBlock uint64
	}

	val := &c{
		BBlock: new(big.Int).SetInt64(1),
	}
	val2 := &d{
		DBlock: 10,
	}

	expect := []*proto.StatusResponse_Fork{
		{
			Name:     "A",
			Disabled: true,
		},
		{
			Name:  "B",
			Block: 1,
		},
		{
			Name:  "D",
			Block: 10,
		},
	}

	res := gatherForks(val, val2)
	assert.Equal(t, res, expect)
}

func TestChainSetHeadReturnsRefusal(t *testing.T) {
	t.Parallel()

	config := DefaultConfig()
	config.Developer.Enabled = true
	config.Developer.Period = 2

	srv, err := CreateMockServer(config)
	require.NoError(t, err)
	defer CloseMockServer(srv)

	_, err = srv.ChainSetHead(context.Background(), &proto.ChainSetHeadRequest{Number: 0})
	require.NoError(t, err)

	rawdb.WriteLastPivotNumber(srv.backend.ChainDb(), 10)
	_, err = srv.ChainSetHead(context.Background(), &proto.ChainSetHeadRequest{Number: 5})
	require.ErrorContains(t, err, "snap-sync pivot")
}
