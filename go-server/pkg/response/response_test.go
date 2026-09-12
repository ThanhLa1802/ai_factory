package response

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func run(t *testing.T, fn func(c *gin.Context)) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.GET("/", func(c *gin.Context) { fn(c) })
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	return rec
}

func TestWriteJSON(t *testing.T) {
	rec := run(t, func(c *gin.Context) { WriteJSON(c, http.StatusCreated, gin.H{"id": "x"}) })
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["id"] != "x" {
		t.Fatalf("body = %s, want id=x", rec.Body.String())
	}
}

func TestWriteAPIError(t *testing.T) {
	rec := run(t, func(c *gin.Context) { WriteAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", "bad") })
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	var got struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Error.Code != "INVALID_REQUEST" || got.Error.Message != "bad" {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestWriteOpenAIError(t *testing.T) {
	rec := run(t, func(c *gin.Context) { WriteOpenAIError(c, http.StatusNotFound, "RESOURCE_NOT_FOUND", "nope") })
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	var got struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
			Code    int    `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Error.Type != "RESOURCE_NOT_FOUND" || got.Error.Message != "nope" || got.Error.Code != http.StatusNotFound {
		t.Fatalf("body = %s", rec.Body.String())
	}
}
