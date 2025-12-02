package stake

import (
	autocliv1 "cosmossdk.io/api/cosmos/autocli/v1"
	_ "cosmossdk.io/api/cosmos/crypto/secp256k1" // register so that it shows up in protoregistry.GlobalTypes
	_ "cosmossdk.io/api/cosmos/crypto/secp256r1" // register so that it shows up in protoregistry.GlobalTypes

	"github.com/0xPolygon/heimdall-v2/api/heimdallv2/stake"
)

// AutoCLIOptions returns the auto cli options for the module (query and tx)
func (am AppModule) AutoCLIOptions() *autocliv1.ModuleOptions {
	return &autocliv1.ModuleOptions{
		Query: &autocliv1.ServiceCommandDescriptor{
			Service: stake.Query_ServiceDesc.ServiceName,
			RpcCommandOptions: []*autocliv1.RpcCommandOptions{
				{
					RpcMethod:      "GetCurrentValidatorSet",
					Use:            "current-validator-set",
					Short:          "Query all validators which are currently active in validator set",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{},
				},
				{
					RpcMethod:      "GetSignerByAddress",
					Use:            "signer [val_address]",
					Short:          "Query validator info for given validator address",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "val_address"}},
				},
				{
					RpcMethod:      "GetValidatorById",
					Use:            "validator [id]",
					Short:          "Query validator info for a given validator id",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "id"}},
				},
				{
					RpcMethod: "GetValidatorStatusByAddress",
					Use:       "validator-status [val_address]",
					Short:     "Query validator status for given validator address",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{
						{ProtoField: "val_address"},
					},
				},
				{
					RpcMethod:      "GetTotalPower",
					Use:            "total-power",
					Short:          "Query total power of the validator set",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{},
				},
				{
					RpcMethod:      "IsStakeTxOld",
					Use:            "is-old-tx [txHash] [logIndex]",
					Short:          "Check if a tx is old (already submitted)",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "tx_hash"}, {ProtoField: "log_index"}},
				},
				{
					RpcMethod:      "GetCurrentProposer",
					Use:            "get-current-proposer",
					Short:          "Get the current proposer",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{},
				},
			},
		},
		Tx: &autocliv1.ServiceCommandDescriptor{
			Service:              stake.Msg_ServiceDesc.ServiceName,
			EnhanceCustomCommand: true,
			RpcCommandOptions: []*autocliv1.RpcCommandOptions{
				{
					RpcMethod: "StakeUpdate",
					Use:       "stake-update [valAddress] [valId] [amount] [txHash] [logIndex] [blockNumber] [nonce]",
					Short:     "Update stake for a validator",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{
						{ProtoField: "from"},
						{ProtoField: "val_id"},
						{ProtoField: "new_amount"},
						{ProtoField: "tx_hash"},
						{ProtoField: "log_index"},
						{ProtoField: "block_number"},
						{ProtoField: "nonce"},
					},
				},
				{
					RpcMethod: "ValidatorExit",
					Use:       "validator-exit [valAddress] [valId] [deactivationEpoch] [txHash] [logIndex] [blockNumber] [nonce]",
					Short:     "Exit validator",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{
						{ProtoField: "from"},
						{ProtoField: "val_id"},
						{ProtoField: "deactivation_epoch"},
						{ProtoField: "tx_hash"},
						{ProtoField: "log_index"},
						{ProtoField: "block_number"},
						{ProtoField: "nonce"},
					},
				},
			},
		},
	}
}
