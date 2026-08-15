package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestHandleUIRouting: handleUI route theo path chính xác, không gated (tạo file tạm).
func TestHandleUIRouting(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"index.html", "chat.html", "keys.html"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(f), 0o644); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
	h := &Handler{uiDir: dir}
	for path, want := range map[string]string{"/": "index.html", "/chat": "chat.html", "/keys": "keys.html"} {
		rec := httptest.NewRecorder()
		h.handleUI(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK || rec.Body.String() != want {
			t.Errorf("%s → code %d body %q, want 200 %q", path, rec.Code, rec.Body.String(), want)
		}
	}
	rec := httptest.NewRecorder()
	h.handleUI(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("/nope → code %d, want 404", rec.Code)
	}
}
