package types

import (
	"github.com/0xPolygon/heimdall-v2/sidetxs"
)

func RegisterSideMsgServer(sideCfg sidetxs.SideTxConfigurator, srv sidetxs.SideMsgServer) {
	sidetxs.CommonRegisterSideMsgServer(sideCfg, srv, _Msg_serviceDesc)
}
