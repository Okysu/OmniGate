package control

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSnapshotPayloadFiltersMediaAndKeepsMessages(t *testing.T) {
	raw := []byte(`{"api_key":"test-secret","messages":[{"role":"user","content":[{"type":"text","text":"保留这段"},{"type":"input_image","image_url":"test-image"},{"type":"input_audio","data":"test-audio"}]},{"role":"assistant","tool_calls":[{"function":{"name":"search","arguments":"[{\"query\":\"保留参数\",\"file_data\":\"test-file\"}]"}}]}],"input":[{"type":"reasoning","encrypted_content":"test-encrypted"}]}`)
	payload, truncated := SnapshotPayload(raw)
	for _, want := range []string{"保留这段", "保留参数", "messages", "tool_calls", "search"} {
		if !strings.Contains(payload, want) {
			t.Fatalf("snapshot omitted %q: %s", want, payload)
		}
	}
	if truncated || !json.Valid([]byte(payload)) || strings.Contains(payload, "test-") {
		t.Fatalf("invalid or unfiltered snapshot: %s", payload)
	}
	if got := Extract([]byte(`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"DATA:image/png;base64,secret"}}]}]}`)); got != "" {
		t.Fatalf("image-only request produced classifier text: %s", got)
	}
}

func TestSnapshotPayloadByteLimit(t *testing.T) {
	raw, err := json.Marshal(map[string]string{"prompt": "首段" + strings.Repeat("文本\n", MaxSnapshotBytes/4) + "末段"})
	if err != nil {
		t.Fatal(err)
	}
	payload, truncated := SnapshotPayload(raw)
	if !truncated || len(payload) > MaxSnapshotBytes || !utf8.ValidString(payload) || !json.Valid([]byte(payload)) ||
		!strings.Contains(payload, "首段") || !strings.Contains(payload, "末段") {
		t.Fatalf("invalid byte-bounded snapshot: truncated=%v size=%d", truncated, len(payload))
	}
}
