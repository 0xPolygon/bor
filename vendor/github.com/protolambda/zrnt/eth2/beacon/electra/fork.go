package electra

import (
	"errors"

	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/zrnt/eth2/beacon/deneb"
)

func UpgradeToElectra(spec *common.Spec, epc *common.EpochsContext, pre *deneb.BeaconStateView) (*BeaconStateView, error) {
	return nil, errors.New("upgrade of deneb state to electra state is not supported")
}
