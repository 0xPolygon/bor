package clerk

import (
	autocliv1 "cosmossdk.io/api/cosmos/autocli/v1"
	_ "cosmossdk.io/api/cosmos/crypto/secp256k1" // register to that it shows up in protoregistry.GlobalTypes
	_ "cosmossdk.io/api/cosmos/crypto/secp256r1" // register to that it shows up in protoregistry.GlobalTypes

	"github.com/0xPolygon/heimdall-v2/api/heimdallv2/clerk"
)

// AutoCLIOptions implements the autocli.HasAutoCLIConfig interface.
func (am AppModule) AutoCLIOptions() *autocliv1.ModuleOptions {
	return &autocliv1.ModuleOptions{
		Query: &autocliv1.ServiceCommandDescriptor{
			Service: clerk.Query_ServiceDesc.ServiceName,
			RpcCommandOptions: []*autocliv1.RpcCommandOptions{
				{
					RpcMethod: "GetRecordById",
					Use:       "record [record-id]",
					Short:     "Query a record by its ID.",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{
						{ProtoField: "record_id"},
					},
				},
				{
					RpcMethod: "GetRecordList",
					Use:       "record-list [page] [limit]",
					Short:     "Query record list by page and limit.",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{
						{ProtoField: "page"},
						{ProtoField: "limit"},
					},
				},
				{
					RpcMethod: "GetRecordListWithTime",
					Use:       "record-list-with-time [from-id] [to-time]",
					Short:     "Query record list by time range, page and limit.",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{
						{ProtoField: "from_id"},
						{ProtoField: "to_time"},
					},
				},
				{
					RpcMethod: "GetRecordSequence",
					Use:       "record-sequence [tx-hash] [log-index]",
					Short:     "Query record sequence by tx-hash and log-index.",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{
						{ProtoField: "tx_hash"},
						{ProtoField: "log_index"},
					},
				},
				{
					RpcMethod: "IsClerkTxOld",
					Use:       "is-old-tx [tx-hash] [log-index]",
					Short:     "Check if a tx is old (already submitted).",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{
						{ProtoField: "tx_hash"},
						{ProtoField: "log_index"},
					},
				},
				{
					RpcMethod: "GetLatestRecordId",
					Use:       "latest-record-id",
					Short:     "Query the latest record id from L1.",
				},
				{
					RpcMethod: "GetRecordCount",
					Use:       "record-count",
					Short:     "Query the total number of event records.",
				},
			},
		},
		Tx: &autocliv1.ServiceCommandDescriptor{
			Service: clerk.Msg_ServiceDesc.ServiceName,
			RpcCommandOptions: []*autocliv1.RpcCommandOptions{
				{
					RpcMethod: "HandleMsgEventRecord",
					Use:       "handle-msg-event-record [from] [tx-hash] [log-index] [block-number] [contract-address] [data] [id] [chain-id]",
					Short:     "Adds the state sync event in the DB.",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{
						{ProtoField: "from"},
						{ProtoField: "tx_hash"},
						{ProtoField: "log_index"},
						{ProtoField: "block_number"},
						{ProtoField: "contract_address"},
						{ProtoField: "data"},
						{ProtoField: "id"},
						{ProtoField: "chain_id"},
					},
				},
			},
		},
	}
}
