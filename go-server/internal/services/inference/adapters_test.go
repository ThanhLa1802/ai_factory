package inference

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// dataURL builds an inline base64 data URL for tests.
func dataURL(mime string, payload []byte) string {
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(payload)
}

func TestOpenAIContentPlainString(t *testing.T) {
	var c OpenAIContent
	if err := json.Unmarshal([]byte(`"hello"`), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if c.Text != "hello" || len(c.Images) != 0 {
		t.Errorf("content = %+v, want text hello and no images", c)
	}
}

func TestOpenAIContentPartsTextAndImage(t *testing.T) {
	payload := []byte("\x89PNG\r\n\x1a\nfake")
	body := `[
		{"type":"text","text":"what is this?"},
		{"type":"image_url","image_url":{"url":"` + dataURL("image/png", payload) + `"}}
	]`
	var c OpenAIContent
	if err := json.Unmarshal([]byte(body), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if c.Text != "what is this?" {
		t.Errorf("text = %q", c.Text)
	}
	if len(c.Images) != 1 {
		t.Fatalf("images = %d, want 1", len(c.Images))
	}
	if c.Images[0].MimeType != "image/png" {
		t.Errorf("mime = %q", c.Images[0].MimeType)
	}
	if string(c.Images[0].Data) != string(payload) {
		t.Error("decoded image bytes differ")
	}
}

func TestOpenAIContentPartsRejects(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "remote url",
			body: `[{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]`,
			want: "inline data: URL",
		},
		{
			name: "missing url",
			body: `[{"type":"image_url","image_url":{}}]`,
			want: "image_url.url is required",
		},
		{
			name: "unsupported mime",
			body: `[{"type":"image_url","image_url":{"url":"` + dataURL("image/gif", []byte("gif")) + `"}}]`,
			want: "unsupported image type",
		},
		{
			name: "not base64",
			body: `[{"type":"image_url","image_url":{"url":"data:image/png,abc"}}]`,
			want: "base64",
		},
		{
			name: "bad base64",
			body: `[{"type":"image_url","image_url":{"url":"data:image/png;base64,!!!!"}}]`,
			want: "decode image base64",
		},
		{
			name: "empty payload",
			body: `[{"type":"image_url","image_url":{"url":"data:image/png;base64,"}}]`,
			want: "image is empty",
		},
		{
			name: "unknown part",
			body: `[{"type":"audio","text":"x"}]`,
			want: "unsupported content part type",
		},
		{
			name: "not string or array",
			body: `42`,
			want: "string or an array",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var c OpenAIContent
			err := json.Unmarshal([]byte(tc.body), &c)
			if err == nil {
				t.Fatal("unmarshal succeeded, want an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestOpenAIToInternalCarriesImages(t *testing.T) {
	body := `{
		"model":"qwen2.5-vl-7b",
		"messages":[
			{"role":"system","content":"be brief"},
			{"role":"user","content":[
				{"type":"text","text":"describe"},
				{"type":"image_url","image_url":{"url":"` + dataURL("image/jpeg", []byte("jpeg")) + `"}}
			]}
		]
	}`
	var req OpenAIRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	msgs, system, err := OpenAIToInternal(&req)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if system != "be brief" {
		t.Errorf("system = %q", system)
	}
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want 1", len(msgs))
	}
	if msgs[0].Content != "describe" || len(msgs[0].Images) != 1 {
		t.Errorf("msg = %+v, want text + 1 image", msgs[0])
	}
	if msgs[0].Images[0].MimeType != "image/jpeg" {
		t.Errorf("mime = %q", msgs[0].Images[0].MimeType)
	}
}
