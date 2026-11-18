package control

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"omnigate/internal/platform/db"
)

const MaxSnapshotBytes = 1 << 20

type Snapshot struct {
	ID        uuid.UUID       `json:"id"`
	RequestID string          `json:"requestId"`
	Model     string          `json:"model"`
	Dialect   string          `json:"dialect"`
	Payload   json.RawMessage `json:"payload"`
	Truncated bool            `json:"truncated"`
	CreatedAt time.Time       `json:"createdAt"`
}

// SnapshotPayload preserves text, messages and tool call arguments while removing
// media fields, attachments and encrypted reasoning. The bound includes JSON escaping.
func SnapshotPayload(raw []byte) (string, bool) {
	var root map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if dec.Decode(&root) != nil {
		return "{}", false
	}
	out := map[string]any{}
	for _, key := range []string{"messages", "input", "system", "instructions", "prompt", "suffix", "query", "documents", "state"} {
		if value := cleanSnapshotValue(root[key]); value != nil {
			out[key] = value
		}
	}
	payload, _ := json.Marshal(out)
	if len(payload) <= MaxSnapshotBytes {
		return string(payload), false
	}

	// A structured JSON document cannot be sliced safely. Use the first and last
	// parts of its serialized text and mark the resulting snapshot as truncated.
	runes := []rune(string(payload))
	hi := min(len(runes), MaxSnapshotBytes)
	lo := 0
	encode := func(n int) []byte {
		head := n / 4
		text := string(runes[:head]) + "\n[snapshot truncated to 1 MiB]\n" + string(runes[len(runes)-(n-head):])
		b, _ := json.Marshal(map[string]string{"text": text})
		return b
	}
	for lo < hi {
		n := (lo + hi + 1) / 2
		if len(encode(n)) <= MaxSnapshotBytes {
			lo = n
		} else {
			hi = n - 1
		}
	}
	return string(encode(lo)), true
}

func cleanSnapshotValue(value any) any {
	switch v := value.(type) {
	case string:
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(v)), "data:") {
			return nil
		}
		return v
	case []any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			if cleaned := cleanSnapshotValue(item); cleaned != nil {
				out = append(out, cleaned)
			}
		}
		return out
	case map[string]any:
		switch v["type"] {
		case "image", "input_image", "image_url", "input_audio", "audio", "video", "video_url", "input_file", "file", "document", "base64", "thinking", "redacted_thinking", "reasoning":
			return nil
		}
		out := make(map[string]any, len(v))
		for key, item := range v {
			switch strings.ToLower(key) {
			case "image_url", "image", "images", "audio", "video", "attachments", "file", "file_data", "b64_json", "base64", "binary", "encrypted_content", "signature":
				continue
			case "arguments", "output":
				if str, ok := item.(string); ok {
					item = cleanToolJSON(str)
				}
			}
			if cleaned := cleanSnapshotValue(item); cleaned != nil {
				out[key] = cleaned
			}
		}
		return out
	default:
		return value
	}
}

// Tool arguments and results can carry structured JSON as a string.
func cleanToolJSON(text string) string {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
		return text
	}
	var value any
	dec := json.NewDecoder(strings.NewReader(trimmed))
	dec.UseNumber()
	if !json.Valid([]byte(trimmed)) || dec.Decode(&value) != nil {
		return text
	}
	cleaned, _ := json.Marshal(cleanSnapshotValue(value))
	return string(cleaned)
}

func saveSnapshot(ctx context.Context, tx db.Tx, in Input, window time.Duration) (uuid.UUID, error) {
	// Lock the user so concurrent matches cannot capture two snapshots in
	// the same rolling window. SQLite serializes writes in db.InTx.
	var user uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, in.UserID).Scan(&user); err != nil {
		return uuid.Nil, err
	}
	var id uuid.UUID
	now := time.Now().UTC()
	err := tx.QueryRow(ctx, `SELECT id FROM control_snapshots WHERE user_id=$1 AND created_at>$2 ORDER BY created_at DESC,id DESC LIMIT 1`, in.UserID, now.Add(-window)).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !db.IsNoRows(err) {
		return uuid.Nil, err
	}
	id = uuid.Must(uuid.NewV7())
	payload, truncated := SnapshotPayload(in.Body)
	_, err = tx.Exec(ctx, `INSERT INTO control_snapshots(id,user_id,request_id,model,dialect,payload,truncated,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, id, in.UserID, in.RequestID, in.Model, in.Dialect, payload, truncated, now)
	return id, err
}

// Keep snapshots that can still be reused by a current window, or are referenced
// by a retained event. Prune after trimming events to bound retained content.
func pruneSnapshots(ctx context.Context, tx db.Tx) error {
	_, err := tx.Exec(ctx, `DELETE FROM control_snapshots WHERE created_at <= $1 AND NOT EXISTS
		(SELECT 1 FROM control_events WHERE snapshot_id=control_snapshots.id)`, time.Now().UTC().Add(-24*time.Hour))
	return err
}
