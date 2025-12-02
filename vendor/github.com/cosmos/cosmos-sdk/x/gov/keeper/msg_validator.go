package keeper

import (
	"fmt"

	authv1beta1 "cosmossdk.io/api/cosmos/auth/v1beta1"
	bankv1beta1 "cosmossdk.io/api/cosmos/bank/v1beta1"
	consensusv1 "cosmossdk.io/api/cosmos/consensus/v1"
	govv1 "cosmossdk.io/api/cosmos/gov/v1"
	errorsmod "cosmossdk.io/errors"
	"github.com/0xPolygon/heimdall-v2/api/heimdallv2/bor"
	"github.com/0xPolygon/heimdall-v2/api/heimdallv2/chainmanager"
	"github.com/0xPolygon/heimdall-v2/api/heimdallv2/checkpoint"
	"github.com/0xPolygon/heimdall-v2/api/heimdallv2/milestone"
	borTypes "github.com/0xPolygon/heimdall-v2/x/bor/types"
	chainmanagertypes "github.com/0xPolygon/heimdall-v2/x/chainmanager/types"
	checkpointTypes "github.com/0xPolygon/heimdall-v2/x/checkpoint/types"
	milestoneTypes "github.com/0xPolygon/heimdall-v2/x/milestone/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	consensustypes "github.com/cosmos/cosmos-sdk/x/consensus/types"
	"github.com/cosmos/cosmos-sdk/x/gov/types"
	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
)

// ValidateGovMsgType validates the type of the message submitted within a proposal.
// It only accepts `MsgExecLegacyContent` and `MsgUpdateParams` for the heimdall-v2 enabled modules.
func ValidateGovMsgType(msg sdk.Msg) error {
	switch msg.(type) {
	case *v1.MsgExecLegacyContent,
		*v1.MsgUpdateParams, *govv1.MsgUpdateParams,
		*authtypes.MsgUpdateParams, *authv1beta1.MsgUpdateParams,
		*banktypes.MsgUpdateParams, *bankv1beta1.MsgUpdateParams,
		*consensustypes.MsgUpdateParams, *consensusv1.MsgUpdateParams,
		// HV2: list of MsgUpdateParams for heimdall-v2 custom modules, to be eventually extended
		*chainmanagertypes.MsgUpdateParams, *chainmanager.MsgUpdateParams,
		*borTypes.MsgUpdateParams, *bor.MsgUpdateParams,
		*checkpointTypes.MsgUpdateParams, *checkpoint.MsgUpdateParams,
		*milestoneTypes.MsgUpdateParams, *milestone.MsgUpdateParams:
		return nil
	default:
		return errorsmod.Wrap(types.ErrInvalidProposalMsgType, fmt.Sprintf("type not supported: %T", msg))
	}
}

// ValidateGovMsgContentType validates the type of the msg content submitted within a proposal.
// It only accepts `TextProposal` and `ParamChange` for the heimdall-v2 enabled modules.
func ValidateGovMsgContentType(msg *v1.MsgExecLegacyContent) error {
	switch msg.Content.TypeUrl {
	// HV2: list of Proposals for heimdall-v2 custom modules, to be eventually extended
	case "/cosmos.gov.v1beta1.TextProposal",
		"/cosmos.params.v1beta1.ParameterChangeProposal", "/cosmos.params.v1beta1.ParamChange":
		return nil
	default:
		return errorsmod.Wrap(types.ErrInvalidProposalContentType, fmt.Sprintf("type not supported: %T", msg.Content))
	}
}
