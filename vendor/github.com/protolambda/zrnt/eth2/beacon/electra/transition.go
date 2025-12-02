package electra

import (
	"context"
	"errors"

	"github.com/protolambda/zrnt/eth2/beacon/common"
)

func (state *BeaconStateView) ProcessEpoch(ctx context.Context, spec *common.Spec, epc *common.EpochsContext) error {
	return errors.New("electra epoch processing is not supported")
}

func (state *BeaconStateView) ProcessBlock(ctx context.Context, spec *common.Spec, epc *common.EpochsContext, benv *common.BeaconBlockEnvelope) error {
	return errors.New("electra block processing is not supported")
}
