package types

// x/topup module event types
const (
	AttributeValueCategory        = ModuleName
	EventTypeTopup                = "topup"
	EventTypeFeeWithdraw          = "fee-withdraw"
	EventTypeWithdraw             = "withdraw"
	AttributeKeyRecipient         = "recipient"
	AttributeKeySender            = "sender"
	AttributeKeyUser              = "user"
	AttributeKeyTopupAmount       = "topup-amount"
	AttributeKeyFeeWithdrawAmount = "fee-withdraw-amount"
)
