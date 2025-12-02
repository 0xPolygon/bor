package types

import "cosmossdk.io/errors"

var ErrInvalidParams = errors.Register(ModuleName, 1, "invalid params")
