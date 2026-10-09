// Package plaza implements model display information and the model plaza
// (docs/contracts/phase5-api.md §2–§3): administrators describe logical
// models; users browse the platform's models and the models they can call.
package plaza

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"omnigate/internal/apperr"
	"omnigate/internal/audit"
	"omnigate/internal/platform/db"
)

// Audit actions.
const (
	ActionInfoUpdate = "model_info.update"
	ActionInfoDelete = "model_info.delete"
)

// Limits from the contract.
const (
	maxModelLen       = 128
	maxDisplayNameLen = 100
	maxDescriptionLen = 1000
	maxVendorLen      = 50
	maxTags           = 10
	maxTagLen         = 20
	maxTokens         = 1<<53 - 1 // largest integer JavaScript represents exactly
	maxSortOrder      = 999_999_999
)

// Capabilities are the boolean feature flags of a model.
type Capabilities struct {
	Vision    bool `json:"vision"`
	Tools     bool `json:"tools"`
	Reasoning bool `json:"reasoning"`
	Embedding bool `json:"embedding"`
	// ImageGeneration marks image models (/v1/images/*; phase7-api.md §1).
	ImageGeneration bool `json:"imageGeneration"`
	// AudioInput / AudioOutput mark audio models: speech recognition
	// (/v1/audio/transcriptions, /translations) and speech synthesis
	// (/v1/audio/speech) (phase9-api.md §1).
	AudioInput  bool `json:"audioInput"`
	AudioOutput bool `json:"audioOutput"`
}

// Info is the display information of a logical model.
type Info struct {
	Model         string       `json:"model"`
	DisplayName   string       `json:"displayName"`
	Description   string       `json:"description"`
	Vendor        string       `json:"vendor"`
	Tags          []string     `json:"tags"`
	ContextWindow *int64       `json:"contextWindow"`
	MaxOutput     *int64       `json:"maxOutput"`
	Capabilities  Capabilities `json:"capabilities"`
	Hidden        bool         `json:"hidden"`
	SortOrder     int          `json:"sortOrder"`
	Version       int          `json:"version"`
	UpdatedAt     time.Time    `json:"updatedAt"`
}

// Input is the body of PUT /api/admin/model-info/{model}. Fields are kept raw
// so that type errors are reported per field (422 details); absent fields
// take their defaults (the PUT replaces the whole row).
type Input struct {
	DisplayName   json.RawMessage `json:"displayName"`
	Description   json.RawMessage `json:"description"`
	Vendor        json.RawMessage `json:"vendor"`
	Tags          json.RawMessage `json:"tags"`
	ContextWindow json.RawMessage `json:"contextWindow"`
	MaxOutput     json.RawMessage `json:"maxOutput"`
	Capabilities  json.RawMessage `json:"capabilities"`
	Hidden        json.RawMessage `json:"hidden"`
	SortOrder     json.RawMessage `json:"sortOrder"`
	// Version is required when the row exists and must be absent otherwise.
	Version *int `json:"version"`
}

func isNull(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) == 0 || bytes.Equal(t, []byte("null"))
}

func parseText(raw json.RawMessage, key string, maxLen int, details map[string]any) string {
	if isNull(raw) {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		details[key] = "必须是字符串"
		return ""
	}
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > maxLen {
		details[key] = fmt.Sprintf("不能超过 %d 个字符", maxLen)
	}
	return s
}

func parseTokens(raw json.RawMessage, key string, details map[string]any) *int64 {
	if isNull(raw) {
		return nil
	}
	var n json.Number
	if t := bytes.TrimSpace(raw); t[0] == '"' || json.Unmarshal(t, &n) != nil {
		details[key] = "必须是正整数或 null"
		return nil
	}
	v, err := n.Int64()
	if err != nil || v < 1 || v > maxTokens {
		details[key] = "必须是正整数或 null"
		return nil
	}
	return &v
}

// validate checks in for model and returns the row it describes.
func (in *Input) validate(model string) (*Info, error) {
	details := map[string]any{}
	if model == "" || strings.TrimSpace(model) != model || utf8.RuneCountInString(model) > maxModelLen {
		details["model"] = fmt.Sprintf("模型名长度应为 1–%d 个字符，且不能有首尾空格", maxModelLen)
	}
	info := &Info{Model: model, Tags: []string{}}
	info.DisplayName = parseText(in.DisplayName, "displayName", maxDisplayNameLen, details)
	info.Description = parseText(in.Description, "description", maxDescriptionLen, details)
	info.Vendor = parseText(in.Vendor, "vendor", maxVendorLen, details)
	if !isNull(in.Tags) {
		var raw []json.RawMessage
		if json.Unmarshal(in.Tags, &raw) != nil {
			details["tags"] = "必须是字符串数组"
		}
		seen := map[string]bool{}
		for i, r := range raw {
			var t string
			if json.Unmarshal(r, &t) != nil {
				details[fmt.Sprintf("tags[%d]", i)] = "必须是字符串"
				continue
			}
			t = strings.TrimSpace(t)
			if utf8.RuneCountInString(t) > maxTagLen {
				details[fmt.Sprintf("tags[%d]", i)] = fmt.Sprintf("每个标签不能超过 %d 个字符", maxTagLen)
				continue
			}
			// Empty tags are dropped; duplicates (ignoring case) keep the first spelling.
			if k := strings.ToLower(t); t != "" && !seen[k] {
				seen[k] = true
				info.Tags = append(info.Tags, t)
			}
		}
		if len(info.Tags) > maxTags {
			details["tags"] = fmt.Sprintf("最多 %d 个标签", maxTags)
		}
	}
	info.ContextWindow = parseTokens(in.ContextWindow, "contextWindow", details)
	info.MaxOutput = parseTokens(in.MaxOutput, "maxOutput", details)
	if info.ContextWindow != nil && info.MaxOutput != nil && *info.MaxOutput > *info.ContextWindow {
		details["maxOutput"] = "最大输出不能超过上下文长度"
	}
	if !isNull(in.Capabilities) {
		var caps map[string]json.RawMessage
		if json.Unmarshal(in.Capabilities, &caps) != nil {
			details["capabilities"] = "必须是对象"
		}
		for k, v := range caps {
			var b bool
			if !slices.Contains([]string{"vision", "tools", "reasoning", "embedding", "imageGeneration", "audioInput", "audioOutput"}, k) || json.Unmarshal(v, &b) != nil {
				details["capabilities"] = "只能包含 vision、tools、reasoning、embedding、imageGeneration、audioInput、audioOutput 七个布尔字段"
				break
			}
			switch k {
			case "vision":
				info.Capabilities.Vision = b
			case "tools":
				info.Capabilities.Tools = b
			case "reasoning":
				info.Capabilities.Reasoning = b
			case "embedding":
				info.Capabilities.Embedding = b
			case "imageGeneration":
				info.Capabilities.ImageGeneration = b
			case "audioInput":
				info.Capabilities.AudioInput = b
			case "audioOutput":
				info.Capabilities.AudioOutput = b
			}
		}
	}
	if !isNull(in.Hidden) && json.Unmarshal(in.Hidden, &info.Hidden) != nil {
		details["hidden"] = "必须是布尔值"
	}
	if !isNull(in.SortOrder) {
		var n json.Number
		v, err := int64(0), error(nil)
		if t := bytes.TrimSpace(in.SortOrder); t[0] == '"' || json.Unmarshal(t, &n) != nil {
			err = fmt.Errorf("not a number")
		} else {
			v, err = n.Int64()
		}
		if err != nil || v < -maxSortOrder || v > maxSortOrder {
			details["sortOrder"] = fmt.Sprintf("必须是 %d 到 %d 之间的整数", -maxSortOrder, maxSortOrder)
		}
		info.SortOrder = int(v)
	}
	if len(details) > 0 {
		return nil, apperr.Validation("模型资料校验失败", details)
	}
	return info, nil
}

// ValidateInput validates in for model exactly like Put and returns the row
// it describes (Version and UpdatedAt unset), without writing.
func ValidateInput(model string, in Input) (*Info, error) { return in.validate(model) }

// SameContent reports whether two rows show the same information (ignoring
// Version and UpdatedAt).
func SameContent(a, b *Info) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Model == b.Model && a.DisplayName == b.DisplayName && a.Description == b.Description &&
		a.Vendor == b.Vendor && slices.Equal(a.Tags, b.Tags) && sameTokens(a.ContextWindow, b.ContextWindow) &&
		sameTokens(a.MaxOutput, b.MaxOutput) && a.Capabilities == b.Capabilities && a.Hidden == b.Hidden &&
		a.SortOrder == b.SortOrder
}

func sameTokens(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// Actor and RequestMeta identify who changes model information (audit).
// The nil ID is the system actor (`omnigate seed`): no updated_by row.
type Actor struct {
	ID   uuid.UUID
	Name string
}

type RequestMeta struct {
	IPPrefix  string
	RequestID string
}

// InfoService stores model information.
type InfoService struct {
	pool *db.DB
	rec  *audit.Recorder
}

func NewInfoService(pool *db.DB, rec *audit.Recorder) *InfoService {
	return &InfoService{pool: pool, rec: rec}
}

const infoCols = `model, display_name, description, vendor, tags, context_window, max_output, capabilities, hidden,
	sort_order, version, updated_at`

func scanInfo(row db.Row) (*Info, error) {
	var i Info
	if err := row.Scan(&i.Model, &i.DisplayName, &i.Description, &i.Vendor, &i.Tags, &i.ContextWindow, &i.MaxOutput,
		&i.Capabilities, &i.Hidden, &i.SortOrder, &i.Version, &i.UpdatedAt); err != nil {
		return nil, err
	}
	if i.Tags == nil {
		i.Tags = []string{}
	}
	i.UpdatedAt = i.UpdatedAt.UTC()
	return &i, nil
}

// List returns every row ordered like the plaza (sortOrder, then model).
func (s *InfoService) List(ctx context.Context) ([]*Info, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+infoCols+` FROM model_info ORDER BY sort_order, model`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Info{}
	for rows.Next() {
		i, err := scanInfo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// All returns every row keyed by model.
func (s *InfoService) All(ctx context.Context) (map[string]*Info, error) {
	list, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]*Info, len(list))
	for _, i := range list {
		out[i.Model] = i
	}
	return out, nil
}

func auditView(i *Info) map[string]any {
	if i == nil {
		return nil
	}
	return map[string]any{"displayName": i.DisplayName, "description": i.Description, "vendor": i.Vendor, "tags": i.Tags,
		"contextWindow": i.ContextWindow, "maxOutput": i.MaxOutput, "capabilities": i.Capabilities, "hidden": i.Hidden,
		"sortOrder": i.SortOrder}
}

// Put creates (in.Version absent) or replaces (in.Version = current version)
// the information of model. created reports whether a row was inserted.
func (s *InfoService) Put(ctx context.Context, a Actor, model string, in Input, m RequestMeta) (out *Info, created bool, err error) {
	next, err := in.validate(model)
	if err != nil {
		return nil, false, err
	}
	tags, _ := json.Marshal(next.Tags)
	caps, _ := json.Marshal(next.Capabilities)
	now := time.Now().UTC()
	var updatedBy *uuid.UUID
	if a.ID != uuid.Nil {
		updatedBy = &a.ID
	}
	err = db.InTx(ctx, s.pool, func(tx db.Tx) error {
		before, err := scanInfo(tx.QueryRow(ctx, `SELECT `+infoCols+` FROM model_info WHERE model = $1 FOR UPDATE`, model))
		switch {
		case db.IsNoRows(err):
			before = nil
		case err != nil:
			return err
		}
		switch {
		case before == nil && in.Version != nil, // deleted meanwhile
			before != nil && (in.Version == nil || *in.Version != before.Version): // created or changed meanwhile
			return apperr.VersionConflict()
		}
		if before == nil {
			created = true
			_, err = tx.Exec(ctx, `INSERT INTO model_info (model, display_name, description, vendor, tags, context_window,
				max_output, capabilities, hidden, sort_order, version, created_at, updated_at, updated_by)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 1, $11, $11, $12)`,
				model, next.DisplayName, next.Description, next.Vendor, tags, next.ContextWindow, next.MaxOutput, caps,
				next.Hidden, next.SortOrder, now, updatedBy)
		} else {
			_, err = tx.Exec(ctx, `UPDATE model_info SET display_name = $2, description = $3, vendor = $4, tags = $5,
				context_window = $6, max_output = $7, capabilities = $8, hidden = $9, sort_order = $10,
				version = version + 1, updated_at = $11, updated_by = $12 WHERE model = $1`,
				model, next.DisplayName, next.Description, next.Vendor, tags, next.ContextWindow, next.MaxOutput, caps,
				next.Hidden, next.SortOrder, now, updatedBy)
		}
		if err != nil {
			return err
		}
		if out, err = scanInfo(tx.QueryRow(ctx, `SELECT `+infoCols+` FROM model_info WHERE model = $1`, model)); err != nil {
			return err
		}
		rid := model
		return s.rec.Record(ctx, tx, audit.Entry{ActorID: &a.ID, ActorName: &a.Name, Action: ActionInfoUpdate,
			ResourceType: "model_info", ResourceID: &rid, IPPrefix: m.IPPrefix, RequestID: m.RequestID,
			Metadata: map[string]any{"created": created, "before": auditView(before), "after": auditView(out)}})
	})
	if err != nil {
		return nil, false, err
	}
	return out, created, nil
}

// Delete removes the information of model.
func (s *InfoService) Delete(ctx context.Context, a Actor, model string, m RequestMeta) error {
	return db.InTx(ctx, s.pool, func(tx db.Tx) error {
		before, err := scanInfo(tx.QueryRow(ctx, `SELECT `+infoCols+` FROM model_info WHERE model = $1 FOR UPDATE`, model))
		if db.IsNoRows(err) {
			return apperr.NotFound("模型资料")
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM model_info WHERE model = $1`, model); err != nil {
			return err
		}
		rid := model
		return s.rec.Record(ctx, tx, audit.Entry{ActorID: &a.ID, ActorName: &a.Name, Action: ActionInfoDelete,
			ResourceType: "model_info", ResourceID: &rid, IPPrefix: m.IPPrefix, RequestID: m.RequestID,
			Metadata: map[string]any{"before": auditView(before)}})
	})
}
