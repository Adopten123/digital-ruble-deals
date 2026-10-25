package documentfile

import (
	"fmt"
	"os"
)

// OpenReadOnlyStore never creates missing directories or publishes files.
// Missing evidence must remain unavailable, not become a fresh empty store.
func OpenReadOnlyStore(directory string) (*Store, error) {
	if directory == "" {
		return nil, fmt.Errorf("document directory is required")
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("document directory must be a real directory")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	return &Store{root: root, readOnly: true}, nil
}
