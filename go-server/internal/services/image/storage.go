package image

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Storage persists generated PNGs. The service hands it an opaque id; it
// returns the stored path (recorded on the asset row).
type Storage interface {
	Save(id string, data []byte) (string, error)
	Load(path string) ([]byte, error)
	Delete(path string) error
}

// DiskStorage writes PNGs under a directory on the local filesystem.
type DiskStorage struct{ dir string }

// NewDiskStorage builds a disk-backed store rooted at dir.
func NewDiskStorage(dir string) *DiskStorage { return &DiskStorage{dir: dir} }

func (s *DiskStorage) Save(id string, data []byte) (string, error) {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return "", fmt.Errorf("create image dir: %w", err)
	}
	path := filepath.Join(s.dir, id+".png")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", fmt.Errorf("write image: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", fmt.Errorf("commit image: %w", err)
	}
	return path, nil
}

func (s *DiskStorage) Load(path string) ([]byte, error) {
	if err := s.within(path); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

func (s *DiskStorage) Delete(path string) error {
	if err := s.within(path); err != nil {
		return err
	}
	return os.Remove(path)
}

// within guards against serving or deleting a path outside the storage root
// (defense-in-depth; ids are generated UUIDs).
func (s *DiskStorage) within(path string) error {
	root, err := filepath.Abs(s.dir)
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if abs != root && !strings.HasPrefix(abs, root+string(os.PathSeparator)) {
		return fmt.Errorf("path escapes image storage: %s", path)
	}
	return nil
}
