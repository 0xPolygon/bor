package types

import "cosmossdk.io/errors"

var ErrNoMilestoneFound = errors.Register(ModuleName, 3, "milestone not found")
