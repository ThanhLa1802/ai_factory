package inference

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testPNG returns a valid 1x1 PNG, encoded on the fly so the store's decode
// validation is exercised against real image bytes.
func testPNG(t *testing.T, c color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, c)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func TestDiskImageStoreSaveLoad(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s := NewDiskImageStore(dir, 0)

	data := testPNG(t, color.RGBA{R: 255, A: 255})
	ref, err := s.Save(ctx, data, "image/png")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if len(ref.ID) != 64 {
		t.Errorf("id = %q, want a 64-char sha256 hex", ref.ID)
	}
	if ref.MimeType != "image/png" || ref.Size != int64(len(data)) {
		t.Errorf("ref = %+v, want mime image/png size %d", ref, len(data))
	}

	got, err := s.Load(ctx, ref)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Error("loaded bytes differ from saved bytes")
	}

	byID, mime, err := s.LoadByID(ctx, ref.ID)
	if err != nil {
		t.Fatalf("load by id: %v", err)
	}
	if mime != "image/png" || !bytes.Equal(byID, data) {
		t.Errorf("load by id = mime %q, %d bytes", mime, len(byID))
	}
}

func TestDiskImageStoreDedupes(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s := NewDiskImageStore(dir, 0)

	data := testPNG(t, color.RGBA{G: 255, A: 255})
	first, err := s.Save(ctx, data, "image/png")
	if err != nil {
		t.Fatalf("first save: %v", err)
	}
	second, err := s.Save(ctx, data, "image/png")
	if err != nil {
		t.Fatalf("second save: %v", err)
	}
	if first.ID != second.ID {
		t.Errorf("ids differ for identical content: %s vs %s", first.ID, second.ID)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("dir has %d entries, want 1 (content-addressed)", len(entries))
	}
}

func TestDiskImageStoreRejects(t *testing.T) {
	ctx := context.Background()
	s := NewDiskImageStore(t.TempDir(), 1024)

	tests := []struct {
		name string
		data []byte
		mime string
	}{
		{name: "empty", data: nil, mime: "image/png"},
		{name: "unsupported type", data: testPNG(t, color.White), mime: "image/gif"},
		{name: "not an image", data: []byte("not an image"), mime: "image/png"},
		{name: "too large", data: make([]byte, 2048), mime: "image/png"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.Save(ctx, tc.data, tc.mime); err == nil {
				t.Fatal("Save succeeded, want an error")
			}
		})
	}
}

func TestDiskImageStoreTooLargeSentinel(t *testing.T) {
	s := NewDiskImageStore(t.TempDir(), 16)
	_, err := s.Save(context.Background(), testPNG(t, color.Black), "image/png")
	if !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("err = %v, want ErrImageTooLarge", err)
	}
}

func TestDiskImageStoreLoadMissing(t *testing.T) {
	s := NewDiskImageStore(t.TempDir(), 0)
	ref := MessageImage{ID: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", MimeType: "image/png"}
	if _, err := s.Load(context.Background(), ref); !errors.Is(err, ErrImageNotFound) {
		t.Fatalf("Load err = %v, want ErrImageNotFound", err)
	}
	if _, _, err := s.LoadByID(context.Background(), ref.ID); !errors.Is(err, ErrImageNotFound) {
		t.Fatalf("LoadByID err = %v, want ErrImageNotFound", err)
	}
}

// TestPersistImages covers the handler step that turns inline bytes into the
// content-addressed reference a session persists.
func TestPersistImages(t *testing.T) {
	ctx := context.Background()
	h := &Handler{images: NewDiskImageStore(t.TempDir(), 0), maxImages: 2}

	data := testPNG(t, color.RGBA{B: 255, A: 255})
	msgs := []Message{
		{Role: RoleUser, Content: "hi"},
		{Role: RoleUser, Content: "look", Images: []MessageImage{{MimeType: "image/png", Data: data}}},
	}
	if err := h.persistImages(ctx, msgs); err != nil {
		t.Fatalf("persist: %v", err)
	}
	img := msgs[1].Images[0]
	if len(img.ID) != 64 || img.MimeType != "image/png" {
		t.Errorf("persisted ref = %+v, want a sha256 id + mime", img)
	}
	if img.Data != nil {
		t.Error("inline bytes should be dropped once persisted")
	}

	// Already-persisted refs are left untouched (replayed history).
	again := []Message{{Role: RoleUser, Images: []MessageImage{img}}}
	if err := h.persistImages(ctx, again); err != nil {
		t.Fatalf("persist replay: %v", err)
	}
	if again[0].Images[0].ID != img.ID {
		t.Error("replayed image was re-hashed")
	}
}

func TestPersistImagesLimits(t *testing.T) {
	h := &Handler{images: NewDiskImageStore(t.TempDir(), 0), maxImages: 1}
	msgs := []Message{{Role: RoleUser, Images: []MessageImage{
		{MimeType: "image/png", Data: testPNG(t, color.White)},
		{MimeType: "image/png", Data: testPNG(t, color.Black)},
	}}}
	if err := h.persistImages(context.Background(), msgs); err == nil {
		t.Fatal("persist succeeded over maxImages, want an error")
	}
}

func TestPersistImagesWithoutStore(t *testing.T) {
	h := &Handler{}
	msgs := []Message{{Role: RoleUser, Images: []MessageImage{{MimeType: "image/png", Data: []byte("x")}}}}
	if err := h.persistImages(context.Background(), msgs); err == nil {
		t.Fatal("persist succeeded without a store, want an error")
	}
}

// TestToInferenceMessagesLoadsImages: the loop rehydrates bytes from the store
// for history turns, which only persist the id + mime type.
func TestToInferenceMessagesLoadsImages(t *testing.T) {
	ctx := context.Background()
	store := NewDiskImageStore(t.TempDir(), 0)
	data := testPNG(t, color.RGBA{R: 128, A: 255})
	ref, err := store.Save(ctx, data, "image/png")
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	loop := &Loop{images: store}
	out, err := loop.toInferenceMessages(ctx, []Message{
		{Role: RoleUser, Content: "describe", Images: []MessageImage{ref}},
	})
	if err != nil {
		t.Fatalf("toInferenceMessages: %v", err)
	}
	if len(out[0].Images) != 1 {
		t.Fatalf("images = %d, want 1", len(out[0].Images))
	}
	if !bytes.Equal(out[0].Images[0].Data, data) {
		t.Error("rehydrated bytes differ from the stored image")
	}
	if out[0].Images[0].MimeType != "image/png" {
		t.Errorf("mime = %q", out[0].Images[0].MimeType)
	}
}

// TestToInferenceMessagesSkipsMissingImage: a deleted upload must not brick
// every later turn of the conversation.
func TestToInferenceMessagesSkipsMissingImage(t *testing.T) {
	loop := &Loop{images: NewDiskImageStore(t.TempDir(), 0)}
	out, err := loop.toInferenceMessages(context.Background(), []Message{
		{Role: RoleUser, Content: "describe", Images: []MessageImage{
			{ID: strings.Repeat("0", 64), MimeType: "image/png"},
		}},
	})
	if err != nil {
		t.Fatalf("toInferenceMessages: %v", err)
	}
	if len(out[0].Images) != 0 {
		t.Errorf("images = %d, want 0 (missing file skipped)", len(out[0].Images))
	}
}

// TestDiskImageStoreRejectsPathTraversal: an id is a minted sha256, so anything
// else (e.g. ../) must never reach the filesystem.
func TestDiskImageStoreRejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	s := NewDiskImageStore(dir, 0)
	if _, _, err := s.LoadByID(context.Background(), "../secret"); !errors.Is(err, ErrImageNotFound) {
		t.Fatalf("LoadByID err = %v, want ErrImageNotFound", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "..", "secret")); err == nil {
		t.Fatal("unexpected file outside the store")
	}
}
