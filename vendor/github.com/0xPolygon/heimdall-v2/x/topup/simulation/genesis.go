package simulation

import (
	"math"
	"math/big"
	"math/rand"
	"strconv"

	"github.com/cosmos/cosmos-sdk/types/module"
	"github.com/cosmos/cosmos-sdk/types/simulation"

	"github.com/0xPolygon/heimdall-v2/helper"
	"github.com/0xPolygon/heimdall-v2/types"
	topupTypes "github.com/0xPolygon/heimdall-v2/x/topup/types"
)

var SequenceNumber = "sequence_number"

// genSequenceNumber returns a random sequence number
func genSequenceNumber(r *rand.Rand) string {
	return strconv.Itoa(simulation.RandIntBetween(r, 0, math.MaxInt32))
}

// RandomizeGenState returns a simulated topup genesis
func RandomizeGenState(simState *module.SimulationState) {
	minAccounts := int64(1)
	maxAccounts := int64(50)
	numAccounts, err := helper.SecureRandomInt(minAccounts, maxAccounts)
	if err != nil {
		panic(err)
	}
	accounts := simulation.RandomAccounts(simState.Rand, int(numAccounts))

	var (
		sequences        = make([]string, numAccounts)
		dividendAccounts = make([]types.DividendAccount, numAccounts)
		sequenceNumber   string
	)

	for i := 0; i < int(numAccounts); i++ {

		simState.AppParams.GetOrGenerate(SequenceNumber, &sequenceNumber, simState.Rand, func(r *rand.Rand) {
			sequenceNumber = genSequenceNumber(r)
		})

		sequences[i] = sequenceNumber

		// create the dividend account for validator
		dividendAccounts[i] = types.DividendAccount{
			User:      accounts[i].Address.String(),
			FeeAmount: big.NewInt(0).String(),
		}
	}

	topupGenesis := topupTypes.NewGenesisState(sequences, dividendAccounts)
	simState.GenState[topupTypes.ModuleName] = simState.Cdc.MustMarshalJSON(topupGenesis)
}
