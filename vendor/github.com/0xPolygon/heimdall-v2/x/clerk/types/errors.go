package types

import (
	"cosmossdk.io/errors"
)

var (
	ErrEventRecordAlreadySynced = errors.Register(ModuleName, 5400, "Event record already synced")
	ErrSizeExceed               = errors.Register(ModuleName, 5401, "Data size exceed")
	ErrInvalidTxHash            = errors.Register(ModuleName, 5402, "Invalid tx hash")
)
