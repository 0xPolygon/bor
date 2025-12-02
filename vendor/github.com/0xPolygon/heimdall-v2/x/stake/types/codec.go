package types

import (
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/codec/legacy"
	"github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/msgservice"
)

// RegisterLegacyAminoCodec registers the necessary x/stake interfaces and concrete types
// on the provided LegacyAmino codec. These types are used for Amino JSON serialization.
func RegisterLegacyAminoCodec(cdc *codec.LegacyAmino) {
	legacy.RegisterAminoMsg(cdc, &MsgValidatorJoin{}, "heimdallv2/stake/MsgValidatorJoin")
	legacy.RegisterAminoMsg(cdc, &MsgStakeUpdate{}, "heimdall-v2/stake/MsgStakeUpdate")
	legacy.RegisterAminoMsg(cdc, &MsgSignerUpdate{}, "heimdall-v2/stake/MsgSignerUpdate")
	legacy.RegisterAminoMsg(cdc, &MsgValidatorExit{}, "heimdall-v2/stake/MsgValidatorExit")
}

// RegisterInterfaces registers the x/stake interfaces types with the interface registry
func RegisterInterfaces(registry types.InterfaceRegistry) {
	registry.RegisterImplementations((*sdk.Msg)(nil),
		&MsgValidatorJoin{},
		&MsgStakeUpdate{},
		&MsgSignerUpdate{},
		&MsgValidatorExit{},
	)
	msgservice.RegisterMsgServiceDesc(registry, &_Msg_serviceDesc)
}
