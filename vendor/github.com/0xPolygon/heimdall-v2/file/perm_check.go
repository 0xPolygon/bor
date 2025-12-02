package file

import (
	"errors"
	"fmt"
	"os"

	errTypes "github.com/0xPolygon/heimdall-v2/types/error"
)

// PermCheck checks the secret key and the keystore files.
// it verifies whether they are stored with the correct permissions.
func PermCheck(filePath string, validPerm os.FileMode) error {
	// get the path to keystore files
	f, err := os.Stat(filePath)
	if err != nil {

		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("no file exist corresponding to this path: %s", filePath)
		}

		if !errors.Is(err, os.ErrExist) {
			return errTypes.InvalidPermissionsError{File: filePath, Perm: validPerm, Err: err}
		}

	}

	filePerm := f.Mode()
	if filePerm != validPerm {
		return errTypes.InvalidPermissionsError{File: filePath, Perm: validPerm}
	}

	return nil
}
