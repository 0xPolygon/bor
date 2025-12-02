package simulation

import (
	"math/rand"
	"strconv"
	"time"

	"github.com/cosmos/cosmos-sdk/types/module"
	"github.com/cosmos/cosmos-sdk/types/simulation"

	stakeSim "github.com/0xPolygon/heimdall-v2/x/stake/testutil"
	"github.com/0xPolygon/heimdall-v2/x/stake/types"
)

func RandomizedGenState(simState *module.SimulationState) {
	s1 := rand.NewSource(time.Now().UnixNano())
	r1 := rand.New(s1) //nolint
	n := 5
	stakingSequence := make([]string, n)

	for i := range stakingSequence {
		stakingSequence[i] = strconv.Itoa(simulation.RandIntBetween(r1, 1000, 100000))
	}

	randValidators := stakeSim.GenRandomVals(n, 0, 10, uint64(10), false, 1, 0)

	validators := make([]*types.Validator, n)

	for i := range len(randValidators) {
		validators[i] = &randValidators[i]
	}

	validatorSet := types.NewValidatorSet(validators)

	genesisState := types.NewGenesisState(validators, *validatorSet, stakingSequence)
	simState.GenState[types.ModuleName] = simState.Cdc.MustMarshalJSON(genesisState)
}
