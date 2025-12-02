package topup

import (
	autocliv1 "cosmossdk.io/api/cosmos/autocli/v1"
	_ "cosmossdk.io/api/cosmos/crypto/secp256k1" // register so that it shows up in protoregistry.GlobalTypes
	_ "cosmossdk.io/api/cosmos/crypto/secp256r1" // register so that it shows up in protoregistry.GlobalTypes

	"github.com/0xPolygon/heimdall-v2/api/heimdallv2/topup"
)

// AutoCLIOptions returns the auto cli options for the module (query and tx)
func (am AppModule) AutoCLIOptions() *autocliv1.ModuleOptions {
	return &autocliv1.ModuleOptions{
		Query: &autocliv1.ServiceCommandDescriptor{
			Service: topup.Query_ServiceDesc.ServiceName,
			RpcCommandOptions: []*autocliv1.RpcCommandOptions{
				{
					RpcMethod:      "GetTopupTxSequence",
					Use:            "topup-sequence [tx_hash] [log_index]",
					Short:          "Query the sequence of a topup tx",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "tx_hash"}, {ProtoField: "log_index"}},
				},
				{
					RpcMethod:      "IsTopupTxOld",
					Use:            "is-old-tx [tx_hash] [log_index]",
					Short:          "Check if a tx is old (already submitted)",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "tx_hash"}, {ProtoField: "log_index"}},
				},
				{
					RpcMethod:      "GetDividendAccountByAddress",
					Use:            "dividend-account [address]",
					Short:          "Query a dividend account by its address",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "address"}},
				},
				{
					RpcMethod:      "GetDividendAccountRootHash",
					Use:            "dividend-account-root",
					Short:          "Query dividend account root hash",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{},
				},
				{
					RpcMethod:      "GetAccountProofByAddress",
					Use:            "account-proof [address]",
					Short:          "Query account proof",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "address"}},
				},
				{
					RpcMethod: "VerifyAccountProofByAddress",
					Use:       "verify-account-proof [address] [proof]",
					Short:     "Verify account proof",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{
						{ProtoField: "address"}, {ProtoField: "proof"},
					},
				},
			},
		},
		Tx: &autocliv1.ServiceCommandDescriptor{
			Service:              topup.Msg_ServiceDesc.ServiceName,
			EnhanceCustomCommand: false,
			RpcCommandOptions: []*autocliv1.RpcCommandOptions{
				{
					RpcMethod: "HandleTopupTx",
					Use:       "handle-topup-tx [proposer] [user] [fee] [tx_hash] [log_index] [block_number]",
					Short:     "Handle a topup tx",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{
						{ProtoField: "proposer"},
						{ProtoField: "user"},
						{ProtoField: "fee"},
						{ProtoField: "tx_hash"},
						{ProtoField: "log_index"},
						{ProtoField: "block_number"},
					},
				},
				{
					RpcMethod: "WithdrawFeeTx",
					Use:       "withdraw-fee [proposer] [amount]",
					Short:     "Withdraw fee",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{
						{ProtoField: "proposer"}, {ProtoField: "amount"},
					},
				},
			},
		},
	}
}
