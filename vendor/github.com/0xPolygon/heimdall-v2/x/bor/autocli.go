package bor

import (
	autocliv1 "cosmossdk.io/api/cosmos/autocli/v1"

	"github.com/0xPolygon/heimdall-v2/api/heimdallv2/bor"
)

func (am AppModule) AutoCLIOptions() *autocliv1.ModuleOptions {
	return &autocliv1.ModuleOptions{
		Query: &autocliv1.ServiceCommandDescriptor{
			Service: bor.Query_ServiceDesc.ServiceName,
			RpcCommandOptions: []*autocliv1.RpcCommandOptions{
				{
					RpcMethod: "GetSpanById",
					Use:       "span-by-id [id]",
					Short:     "Query bor span by id",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{
						{ProtoField: "id"},
					},
				},
				{
					RpcMethod: "GetSpanList",
					Use:       "span-list",
					Short:     "Query list of bor spans",
				},
				{
					RpcMethod: "GetLatestSpan",
					Use:       "latest-span",
					Short:     "Query latest bor span",
				},
				{
					RpcMethod: "GetNextSpanSeed",
					Use:       "next-span-seed [id]",
					Short:     "Query next bor span seed",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{
						{ProtoField: "id"},
					},
				},
				{
					RpcMethod: "GetNextSpan",
					Use:       "next-span",
					Short:     "Query next bor span",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{
						{ProtoField: "span_id"},
						{ProtoField: "start_block"},
						{ProtoField: "bor_chain_id"},
					},
				},
				{
					RpcMethod: "GetBorParams",
					Use:       "params",
					Short:     "Query bor params",
				},
			},
		},
	}
}
