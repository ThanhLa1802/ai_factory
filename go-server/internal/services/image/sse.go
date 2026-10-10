package image

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// sseWriter is a minimal Server-Sent Events writer for the image job progress
// endpoint. The text inference service has its own SSEWriter; services do not
// import each other (design D-P4-2), so image keeps this local, tiny helper.
type sseWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

func newSSEWriter(w http.ResponseWriter) (*sseWriter, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("streaming not supported")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher.Flush()
	return &sseWriter{w: w, flusher: flusher}, nil
}

func (s *sseWriter) send(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", data); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

func (s *sseWriter) sendError(msg string) error {
	if err := s.send(map[string]string{"error": msg}); err != nil {
		return err
	}
	s.done()
	return nil
}

func (s *sseWriter) done() {
	fmt.Fprint(s.w, "data: [DONE]\n\n")
	s.flusher.Flush()
}
