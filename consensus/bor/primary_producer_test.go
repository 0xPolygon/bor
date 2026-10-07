package bor

import (
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/bor/valset"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
)

var (
	producerTestVals = []*valset.Validator{
		{Address: common.HexToAddress("0x1"), VotingPower: 1},
		{Address: common.HexToAddress("0x2"), VotingPower: 1},
	}
	producerTestOutsider = common.HexToAddress("0x3")
)

// newProducerTestChain returns a two-validator chain, the header for block 1,
// and that block's in-turn and backup producers.
func newProducerTestChain(t *testing.T, overrides []params.BlockRangeOverrideValidatorSet) (*core.BlockChain, *Bor, *types.Header, common.Address, common.Address) {
	t.Helper()

	borCfg := borConfigWithDelays(64)
	borCfg.OverrideValidatorSetInRange = overrides
	chain, b := newChainAndBorForTest(t, &fakeSpanner{vals: producerTestVals}, borCfg, false, common.Address{}, uint64(time.Now().Unix())-100)

	genesis := chain.HeaderChain().GetHeaderByNumber(0)
	require.NotNil(t, genesis)
	next := &types.Header{ParentHash: genesis.Hash(), Number: big.NewInt(1)}

	snap, err := b.snapshot(chain.HeaderChain(), next, nil, false)
	require.NoError(t, err)
	primary := snap.ValidatorSet.GetProposer().Address
	backup := producerTestVals[0].Address
	if backup == primary {
		backup = producerTestVals[1].Address
	}

	return chain, b, next, primary, backup
}

func TestIsPrimaryProducer(t *testing.T) {
	t.Parallel()

	chain, b, next, primary, backup := newProducerTestChain(t, nil)
	// Block 0 is always resolvable as a checkpoint, so the unknown parent must
	// sit higher up for the snapshot walk to fail.
	unknownParent := &types.Header{ParentHash: common.HexToHash("0xdead"), Number: big.NewInt(5)}
	noopSign := func(accounts.Account, string, []byte) ([]byte, error) { return nil, nil }

	tests := []struct {
		name           string
		signer         *common.Address // nil leaves no signer configured at all
		header         *types.Header
		wantPrimary    bool
		wantAuthorized bool
	}{
		{name: "no signer configured", header: next},
		{name: "empty signer", signer: &common.Address{}, header: next},
		{name: "in-turn producer", signer: &primary, header: next, wantPrimary: true, wantAuthorized: true},
		{name: "backup producer", signer: &backup, header: next, wantAuthorized: true},
		{name: "not a validator", signer: &producerTestOutsider, header: next},
		{name: "snapshot unavailable", signer: &primary, header: unknownParent},
	}

	// Sequential: each case re-authorizes the shared engine.
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.signer == nil {
				b.authorizedSigner.Store(nil)
			} else {
				b.Authorize(*tt.signer, noopSign)
			}

			require.Equal(t, tt.wantPrimary, b.IsPrimaryProducer(chain.HeaderChain(), tt.header))
			require.Equal(t, tt.wantAuthorized, b.IsAuthorizedSigner(chain.HeaderChain(), tt.header))
		})
	}
}

// Seal checks a validator-set override at the header's number, while the
// succession lookup checks it at the snapshot's number (the parent). A signer
// allowed at only one of the two must be refused, as Seal refuses it.
func TestIsPrimaryProducerValidatorSetOverride(t *testing.T) {
	t.Parallel()

	noopSign := func(accounts.Account, string, []byte) ([]byte, error) { return nil, nil }

	tests := []struct {
		name       string
		start, end uint64
	}{
		{name: "allowed only at the parent", start: 0, end: 0},
		{name: "allowed only at the header", start: 1, end: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			chain, b, next, _, _ := newProducerTestChain(t, []params.BlockRangeOverrideValidatorSet{
				{StartBlock: tt.start, EndBlock: tt.end, Validators: []common.Address{producerTestOutsider}},
			})
			b.Authorize(producerTestOutsider, noopSign)

			require.False(t, b.IsPrimaryProducer(chain.HeaderChain(), next))
			require.False(t, b.IsAuthorizedSigner(chain.HeaderChain(), next))
		})
	}
}
