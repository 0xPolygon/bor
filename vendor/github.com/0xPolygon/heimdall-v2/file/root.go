package file

import "path/filepath"

// Rootify joins a root directory with a path to create an absolute path,
// returning the path unchanged if it's already absolute.
func Rootify(path, root string) string {
	if filepath.IsAbs(path) {
		return path
	}

	return filepath.Join(root, path)
}
