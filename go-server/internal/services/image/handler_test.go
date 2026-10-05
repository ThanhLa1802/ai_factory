package image

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	infraimage "github.com/ai-factory/go-server/internal/infrastructure/image"
	"github.com/gin-gonic/gin"
)

func buildEngine(gen *fakeGenerator, res DeploymentResolver) *gin.Engine {
	gin.SetMode(gin.TestMode)
	svc, _, _, _ := newTestService(gen, res, 2)
	e := gin.New()
	NewHandler(svc, fakeAuth{}).RegisterRoutes(e)
	return e
}

func doJSON(e *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	return w
}

func TestHandleGenerateOpenAIB64Shape(t *testing.T) {
	gen := &fakeGenerator{result: &infraimage.GenerateResult{
		PNG: [][]byte{{1, 2, 3}}, Width: 1024, Height: 1024, Seed: 7, Steps: 25, Scheduler: "default",
	}}
	e := buildEngine(gen, nil)

	w := doJSON(e, http.MethodPost, "/v1/images/generations",
		`{"model":"sdxl-1.0","prompt":"a cat"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	var resp struct {
		Created int64 `json:"created"`
		Data    []struct {
			B64JSON string `json:"b64_json"`
			URL     string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Created == 0 || len(resp.Data) != 1 {
		t.Fatalf("resp = %+v", resp)
	}
	raw, err := base64.StdEncoding.DecodeString(resp.Data[0].B64JSON)
	if err != nil || string(raw) != "\x01\x02\x03" {
		t.Fatalf("b64 = %q, err = %v", resp.Data[0].B64JSON, err)
	}
}

func TestHandleGenerateURLFormat(t *testing.T) {
	e := buildEngine(nil, nil)
	w := doJSON(e, http.MethodPost, "/v1/images/generations",
		`{"model":"sdxl-1.0","prompt":"a cat","response_format":"url"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data []struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Data) != 1 || !strings.HasPrefix(resp.Data[0].URL, "/api/v1/images/assets/") {
		t.Fatalf("url = %+v", resp.Data)
	}
}

func TestHandleGenerateInvalidSize(t *testing.T) {
	e := buildEngine(nil, nil)
	w := doJSON(e, http.MethodPost, "/v1/images/generations",
		`{"model":"sdxl-1.0","prompt":"x","size":"1001x1024"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
}

func TestHandleGenerateModelNotFound(t *testing.T) {
	e := buildEngine(nil, fakeResolver{err: ErrNotFound})
	w := doJSON(e, http.MethodPost, "/v1/images/generations",
		`{"model":"nope","prompt":"x"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
}

func TestHandleAssetRequiresAuth(t *testing.T) {
	e := buildEngine(nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/images/assets/abc", nil)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}
