package types

import (
	errorsmod "cosmossdk.io/errors"
)

var (
	// ErrInvalidMsg is returned if the message is invalid
	ErrInvalidMsg = errorsmod.Register(ModuleName, 2, "invalid message")

	// ErrNoValidator is returned if the respective validator doesn't exist
	ErrNoValidator = errorsmod.Register(ModuleName, 4, "no respective validator found")

	// ErrNoSignerChange returned when the new signer address is same as old one
	ErrNoSignerChange = errorsmod.Register(ModuleName, 5, "new signer is same as old one")

	// ErrValUnBonded is returned when the respective validator is already unBonded
	ErrValUnBonded = errorsmod.Register(ModuleName, 6, "validator already unBonded")
)
