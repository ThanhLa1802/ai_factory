package inference

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // register the JPEG decoder for validation
	_ "image/png"  // register the PNG decoder for validation
	"os"
	"path/filepath"
)

var (
	// ErrImageTooLarge is returned when an upload exceeds the configured cap.
	ErrImageTooLarge = errors.New("image too large")
	// ErrImageNotFound is returned when an upload id has no file on disk.
	ErrImageNotFound = errors.New("image not found")
)

// ImageStore persists chat image uploads and loads them back for inference.
// Files live on disk; the DB only stores the content-addressed id + mime type.
type ImageStore interface {
	Save(ctx context.Context, data []byte, mimeType string) (MessageImage, error)
	Load(ctx context.Context, img MessageImage) ([]byte, error)
	LoadByID(ctx context.Context, id string) ([]byte, string, error)
}

// DiskImageStore stores uploads as <sha256>.<ext> in one directory. The id is
// the content hash, so re-uploading the same image is idempotent.
type DiskImageStore struct {
	dir      string
	maxBytes int64
}

// NewDiskImageStore creates a store rooted at dir. maxBytes <= 0 disables the
// size cap.
func NewDiskImageStore(dir string, maxBytes int64) *DiskImageStore {
	return &DiskImageStore{dir: dir, maxBytes: maxBytes}
}

var imageExtensions = map[string]string{
	"image/png":  ".png",
	"image/jpeg": ".jpg",
}

// Save validates, hashes and writes one image. The write is atomic (temp file
// + rename) so a concurrent reader never sees a partial file.
func (s *DiskImageStore) Save(ctx context.Context, data []byte, mimeType string) (MessageImage, error) {
	_ = ctx
	if len(data) == 0 {
		return MessageImage{}, fmt.Errorf("image is empty")
	}
	ext, ok := imageExtensions[mimeType]
	if !ok {
		return MessageImage{}, fmt.Errorf("unsupported image type %q", mimeType)
	}
	if s.maxBytes > 0 && int64(len(data)) > s.maxBytes {
		return MessageImage{}, fmt.Errorf("%w: %d bytes (max %d)", ErrImageTooLarge, len(data), s.maxBytes)
	}
	if _, _, err := image.DecodeConfig(bytes.NewReader(data)); err != nil {
		return MessageImage{}, fmt.Errorf("invalid %s image: %w", mimeType, err)
	}

	sum := sha256.Sum256(data)
	id := hex.EncodeToString(sum[:])
	ref := MessageImage{ID: id, MimeType: mimeType, Size: int64(len(data))}

	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return MessageImage{}, fmt.Errorf("create upload dir: %w", err)
	}
	path := filepath.Join(s.dir, id+ext)
	if _, err := os.Stat(path); err == nil {
		return ref, nil
	}

	tmp, err := os.CreateTemp(s.dir, ".upload-*")
	if err != nil {
		return MessageImage{}, fmt.Errorf("create temp upload: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return MessageImage{}, fmt.Errorf("write upload: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return MessageImage{}, fmt.Errorf("close upload: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		// Another request may have written the same content-addressed file.
		if _, statErr := os.Stat(path); statErr == nil {
			return ref, nil
		}
		return MessageImage{}, fmt.Errorf("commit upload: %w", err)
	}
	return ref, nil
}

// Load reads the file backing a stored reference.
func (s *DiskImageStore) Load(ctx context.Context, img MessageImage) ([]byte, error) {
	_ = ctx
	if !validImageID(img.ID) {
		return nil, fmt.Errorf("invalid image id")
	}
	ext, ok := imageExtensions[img.MimeType]
	if !ok {
		return nil, fmt.Errorf("unsupported image type %q", img.MimeType)
	}
	data, err := os.ReadFile(filepath.Join(s.dir, img.ID+ext))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrImageNotFound
	}
	return data, err
}

// LoadByID reads an upload without knowing its mime type (the serving endpoint
// only has the id in the URL).
func (s *DiskImageStore) LoadByID(ctx context.Context, id string) ([]byte, string, error) {
	_ = ctx
	if !validImageID(id) {
		return nil, "", ErrImageNotFound
	}
	for mime, ext := range imageExtensions {
		data, err := os.ReadFile(filepath.Join(s.dir, id+ext))
		if err == nil {
			return data, mime, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, "", err
		}
	}
	return nil, "", ErrImageNotFound
}

// validImageID accepts only the lowercase hex sha256 we mint ourselves, which
// also makes path traversal impossible.
func validImageID(id string) bool {
	if len(id) != 64 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}
