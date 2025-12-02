package stake

import (
	"errors"

	cmttypes "github.com/cometbft/cometbft/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/0xPolygon/heimdall-v2/x/stake/keeper"
)

// WriteValidators returns a slice of comet genesis validators.
func WriteValidators(ctx sdk.Context, keeper *keeper.Keeper) (vals []cmttypes.GenesisValidator, returnErr error) {
	validators := keeper.GetAllValidators(ctx)
	for _, validator := range validators {
		pk, err := validator.ConsPubKey()
		if err != nil {
			returnErr = err
			return
		}
		pubKey := secp256k1.PubKey{Key: pk}
		cmtPk, err := cryptocodec.ToCmtPubKeyInterface(&pubKey)
		if err != nil {
			returnErr = err
			return
		}
		if cmtPk == nil {
			returnErr = errors.New("invalid public key")
			return
		}

		vals = append(vals, cmttypes.GenesisValidator{
			Address: sdk.ConsAddress(cmtPk.Address()).Bytes(),
			PubKey:  cmtPk,
			Power:   validator.GetVotingPower(),
			Name:    validator.Signer,
		})
	}

	return
}
