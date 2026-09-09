package api

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// SSEWriter handles Server-Sent Events streaming to the HTTP client.
// Each token is sent immediately — no buffering.
type SSEWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
	done    chan struct{}
	written int
}

// NewSSEWriter creates an SSE writer and writes the initial headers.
func NewSSEWriter(w http.ResponseWriter) (*SSEWriter, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("streaming not supported")
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // Disable nginx buffering
	flusher.Flush()

	return &SSEWriter{
		w:       w,
		flusher: flusher,
		done:    make(chan struct{}),
	}, nil
}

// SendError sends an error event.
func (s *SSEWriter) SendError(errMsg string) error {
	data, _ := json.Marshal(map[string]interface{}{
		"type":  "error",
		"error": map[string]string{"message": errMsg},
	})
	fmt.Fprintf(s.w, "data: %s\n\n", string(data))
	fmt.Fprintf(s.w, "data: [DONE]\n\n")
	s.flusher.Flush()
	return nil
}
