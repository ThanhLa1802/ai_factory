package image

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiskStorageSaveLoadDelete(t *testing.T) {
	dir := t.TempDir()
	s := NewDiskStorage(dir)

	path, err := s.Save("asset-1", []byte("png-bytes"))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if filepath.Dir(path) != dir {
		t.Fatalf("path = %q, want under %q", path, dir)
	}
	got, err := s.Load(path)
	if err != nil || string(got) != "png-bytes" {
		t.Fatalf("Load = %q, %v", got, err)
	}
	if err := s.Delete(path); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file still present: %v", err)
	}
}

func TestDiskStorageRejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	s := NewDiskStorage(dir)

	outside := filepath.Join(filepath.Dir(dir), "secret.png")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if _, err := s.Load(outside); err == nil {
		t.Fatal("Load outside root succeeded, want error")
	}
	if err := s.Delete(outside); err == nil {
		t.Fatal("Delete outside root succeeded, want error")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("guard deleted an outside file: %v", err)
	}
}
