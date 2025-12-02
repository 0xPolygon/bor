package checkpoint

import (
	autocliv1 "cosmossdk.io/api/cosmos/autocli/v1"
	_ "cosmossdk.io/api/cosmos/crypto/secp256k1" // register so that it shows up in protoregistry.GlobalTypes
	_ "cosmossdk.io/api/cosmos/crypto/secp256r1" // register so that it shows up in protoregistry.GlobalTypes

	"github.com/0xPolygon/heimdall-v2/api/heimdallv2/checkpoint"
)

// AutoCLIOptions returns the auto cli options for the module (query and tx)
func (am AppModule) AutoCLIOptions() *autocliv1.ModuleOptions {
	return &autocliv1.ModuleOptions{
		Query: &autocliv1.ServiceCommandDescriptor{
			Service: checkpoint.Query_ServiceDesc.ServiceName,
			RpcCommandOptions: []*autocliv1.RpcCommandOptions{
				{
					RpcMethod:      "GetCheckpointParams",
					Use:            "get-params",
					Short:          "Get checkpoint params",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{},
				},
				{
					RpcMethod:      "GetCheckpointOverview",
					Use:            "get-overview",
					Short:          "Get checkpoint overview",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{},
				},
				{
					RpcMethod:      "GetAckCount",
					Use:            "get-ack-count",
					Short:          "Get checkpoint ack count",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{},
				},
				{
					RpcMethod: "GetCheckpoint",
					Use:       "get-checkpoint [number]",
					Short:     "Get checkpoint based on its number",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{
						{ProtoField: "number"},
					},
				},
				{
					RpcMethod:      "GetCheckpointLatest",
					Use:            "get-checkpoint-latest",
					Short:          "Get the latest checkpoint",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{},
				},
				{
					RpcMethod:      "GetCheckpointBuffer",
					Use:            "get-checkpoint-buffer",
					Short:          "Get the checkpoint buffer",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{},
				},
				{
					RpcMethod:      "GetLastNoAck",
					Use:            "get-last-no-ack",
					Short:          "Get the last no ack",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{},
				},
				{
					RpcMethod:      "GetNextCheckpoint",
					Use:            "get-next-checkpoint",
					Short:          "Get the next checkpoint",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{},
				},
				{
					RpcMethod:      "GetCheckpointList",
					Use:            "get-checkpoint-list",
					Short:          "Get the list of checkpoints",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{},
				},
			},
		},
		Tx: &autocliv1.ServiceCommandDescriptor{
			Service:              checkpoint.Msg_ServiceDesc.ServiceName,
			EnhanceCustomCommand: true,
			RpcCommandOptions: []*autocliv1.RpcCommandOptions{
				{
					RpcMethod: "CheckpointNoAck",
					Use:       "checkpoint-no-ack [from]",
					Short:     "Checkpoint no ack",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{
						{ProtoField: "from"},
					},
				},
				{
					RpcMethod: "CheckpointAck",
					Skip:      true,
				},
				{
					RpcMethod: "Checkpoint",
					Skip:      true,
				},
			},
		},
	}
}
