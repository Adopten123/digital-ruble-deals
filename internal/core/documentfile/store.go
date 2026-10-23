package documentfile

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
)

var blobKey = regexp.MustCompile(`^[0-9a-f]{32}\.pdf$`)

// Store owns a private directory. It never accepts a filename or a path from a
// client. A blob is published atomically only after its contents are durable.
// Database failures may leave unreachable blobs; deleting them after an unknown
// transaction outcome could destroy a committed document, so Put never does so.
type Store struct{ root *os.Root }

func NewStore(directory string) (*Store, error) {
	if directory == "" {
		return nil, fmt.Errorf("document directory is required")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
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
	return &Store{root: root}, nil
}

func (s *Store) Close() error { return s.root.Close() }

func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func (s *Store) Put(data []byte) (key string, err error) {
	if err = Validate(data); err != nil {
		return "", err
	}
	id, err := randomID()
	if err != nil {
		return "", err
	}
	temp := ".pending-" + id
	file, err := s.root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	defer func() { file.Close(); s.root.Remove(temp) }()
	if _, err = file.Write(data); err != nil {
		return "", err
	}
	if err = file.Sync(); err != nil {
		return "", err
	}
	if err = file.Close(); err != nil {
		return "", err
	}
	// Link publishes a complete inode and refuses an existing name on Windows
	// and Unix. Rename would silently replace an existing file on Unix.
	for attempts := 0; attempts < 3; attempts++ {
		id, err = randomID()
		if err != nil {
			return "", err
		}
		key = id + ".pdf"
		if err = s.root.Link(temp, key); err == nil {
			break
		}
		if !errors.Is(err, os.ErrExist) {
			return "", err
		}
	}
	if err != nil {
		return "", err
	}
	if err = s.root.Remove(temp); err != nil {
		return "", err
	}
	if err = syncDirectory(s.root); err != nil {
		return "", err
	}
	return key, nil
}

func (s *Store) Read(key string) ([]byte, error) {
	if !blobKey.MatchString(key) {
		return nil, ErrNotFound
	}
	info, err := s.root.Lstat(key)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, ErrInvalid
	}
	if info.Size() > MaxSize {
		return nil, ErrTooLarge
	}
	file, err := s.root.Open(key)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	actual, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !actual.Mode().IsRegular() || !os.SameFile(info, actual) {
		return nil, ErrInvalid
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxSize {
		return nil, ErrTooLarge
	}
	return data, nil
}
