package api

const (
	// Query API methods.
	GetCurrentValidatorSetMethod      = "GetCurrentValidatorSet"
	GetSignerByAddressMethod          = "GetSignerByAddress"
	GetValidatorByIdMethod            = "GetValidatorById"
	GetValidatorStatusByAddressMethod = "GetValidatorStatusByAddress"
	GetTotalPowerMethod               = "GetTotalPower"
	IsStakeTxOldMethod                = "IsStakeTxOld"
	GetCurrentProposerMethod          = "GetCurrentProposer"
	GetProposersByTimesMethod         = "GetProposersByTimes"

	// Transaction API methods.
	ValidatorJoinMethod = "ValidatorJoin"
	StakeUpdateMethod   = "StakeUpdate"
	SignerUpdateMethod  = "SignerUpdate"
	ValidatorExitMethod = "ValidatorExit"

	// Side message handler methods.
	SideHandleMsgValidatorJoinMethod = "SideHandleMsgValidatorJoin"
	SideHandleMsgStakeUpdateMethod   = "SideHandleMsgStakeUpdate"
	SideHandleMsgSignerUpdateMethod  = "SideHandleMsgSignerUpdate"
	SideHandleMsgValidatorExitMethod = "SideHandleMsgValidatorExit"

	// Post message handler methods.
	PostHandleMsgValidatorJoinMethod = "PostHandleMsgValidatorJoin"
	PostHandleMsgStakeUpdateMethod   = "PostHandleMsgStakeUpdate"
	PostHandleMsgSignerUpdateMethod  = "PostHandleMsgSignerUpdate"
	PostHandleMsgValidatorExitMethod = "PostHandleMsgValidatorExit"
)
