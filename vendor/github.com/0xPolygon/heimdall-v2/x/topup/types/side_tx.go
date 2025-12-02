package types

import (
	"github.com/0xPolygon/heimdall-v2/sidetxs"
)

// RegisterSideMsgServer registers server methods for the x/topup module handlers, based on the sideCfg.
func RegisterSideMsgServer(sideCfg sidetxs.SideTxConfigurator, srv sidetxs.SideMsgServer) {
	sidetxs.CommonRegisterSideMsgServer(sideCfg, srv, _Msg_serviceDesc)
}
