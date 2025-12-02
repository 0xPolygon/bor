package errors

import (
	"os"
)

const InvalidFilePermissionErrMsg = "invalid file permission"

type InvalidPermissionsError struct {
	File string
	Perm os.FileMode
	Err  error
}

func (e InvalidPermissionsError) IsDetailed() bool {
	return e.File != "" && e.Perm != 0
}

func (e InvalidPermissionsError) Error() string {
	var errMsg string

	if e.IsDetailed() {
		errMsg = InvalidFilePermissionErrMsg + " for file " + e.File + " should be " + e.Perm.String()
	}

	if e.Err != nil {
		errMsg = InvalidFilePermissionErrMsg + " \nerr: " + e.Err.Error()
	}

	return errMsg
}
