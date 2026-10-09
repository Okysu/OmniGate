// Package seed applies and exports a declarative catalog — sell / cost
// prices, model display information and subscription plans — through the
// service layer (`omnigate seed`, deploy/seed/README.md). Changes are
// validated and audited exactly like the admin API; the actor is the system
// actor "seed" and every audit entry carries metadata source = "seed".
package seed

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"omnigate/internal/plaza"
	"omnigate/internal/pricing"
	"omnigate/internal/subscription"
)

// Catalog is the seed file. Absent sections are left alone: seeding never
// deletes anything.
type Catalog struct {
	// Currency, when set, must equal the target deployment's settlement
	// currency (amounts are not converted).
	Currency  string           `json:"currency,omitempty"`
	Prices    []PriceEntry     `json:"prices,omitempty"`
	ModelInfo []ModelInfoEntry `json:"modelInfo,omitempty"`
	Plans     []PlanEntry      `json:"plans,omitempty"`
}

// PriceEntry is one price version (POST /api/admin/prices). Amounts are
// decimal strings; "" means 0. The nullable per-million prices fall back to
// inputPerM / outputPerM when null. Cost prices name their channel by name.
type PriceEntry struct {
	Kind             string                 `json:"kind"`
	Model            string                 `json:"model"`
	InputPerM        string                 `json:"inputPerM"`
	OutputPerM       string                 `json:"outputPerM"`
	CacheReadPerM    string                 `json:"cacheReadPerM"`
	CacheWritePerM   string                 `json:"cacheWritePerM"`
	PerRequest       string                 `json:"perRequest"`
	PerImage         string                 `json:"perImage"`
	ImageInputPerM   *string                `json:"imageInputPerM"`
	AudioInputPerM   *string                `json:"audioInputPerM"`
	AudioOutputPerM  *string                `json:"audioOutputPerM"`
	PerMinute        string                 `json:"perMinute"`
	PerMCharacters   string                 `json:"perMCharacters"`
	Schedule         []pricing.ScheduleSlot `json:"schedule"`
	ScheduleTimezone string                 `json:"scheduleTimezone"`
	// Tiers are the context-length tiers (phase10-api.md §1; absent / null =
	// none); tier prices left null inherit the base fields.
	Tiers       []pricing.TierInput `json:"tiers"`
	ChannelName *string             `json:"channelName"`
}

// ModelInfoEntry is the display information of a model
// (PUT /api/admin/model-info/{model}).
type ModelInfoEntry struct {
	Model         string             `json:"model"`
	DisplayName   string             `json:"displayName"`
	Description   string             `json:"description"`
	Vendor        string             `json:"vendor"`
	Tags          []string           `json:"tags"`
	ContextWindow *int64             `json:"contextWindow"`
	MaxOutput     *int64             `json:"maxOutput"`
	Capabilities  plaza.Capabilities `json:"capabilities"`
	Hidden        bool               `json:"hidden"`
	SortOrder     int                `json:"sortOrder"`
}

// PlanEntry is a subscription plan (POST /api/admin/billing/plans); plans are
// matched by exact name. Rules use the admin API's JSON.
type PlanEntry struct {
	Name        string              `json:"name"`
	Description string              `json:"description"`
	ListPrice   *string             `json:"listPrice"`
	Duration    string              `json:"duration"`
	Models      []string            `json:"models"`
	Stackable   bool                `json:"stackable"`
	Status      string              `json:"status"`
	Rules       []subscription.Rule `json:"rules"`
}

// Problem is one validation error at a field path such as
// "plans[2].rules[0].limit".
type Problem struct {
	Path    string
	Message string
}

// ValidationError lists every problem of a catalog; nothing was written.
type ValidationError struct{ Problems []Problem }

func (e *ValidationError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "catalog is invalid (%d problem(s)); nothing was written:", len(e.Problems))
	for _, p := range e.Problems {
		b.WriteString("\n  ")
		if p.Path != "" {
			b.WriteString(p.Path + ": ")
		}
		b.WriteString(p.Message)
	}
	return b.String()
}

type problems []Problem

func (ps *problems) add(path, format string, args ...any) {
	*ps = append(*ps, Problem{Path: path, Message: fmt.Sprintf(format, args...)})
}

// addDetails adds the field details of a service validation error under
// prefix (e.g. "prices[3]"). rename maps service field names to file names.
func (ps *problems) addDetails(prefix string, details map[string]any, rename map[string]string) {
	keys := make([]string, 0, len(details))
	for k := range details {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		name := k
		if r, ok := rename[k]; ok {
			name = r
		}
		ps.add(prefix+"."+name, "%v", details[k])
	}
}

func (ps problems) err() error {
	if len(ps) == 0 {
		return nil
	}
	return &ValidationError{Problems: ps}
}

// rawCatalog keeps the elements raw so that decoding errors carry their index.
type rawCatalog struct {
	Currency  string            `json:"currency"`
	Prices    []json.RawMessage `json:"prices"`
	ModelInfo []json.RawMessage `json:"modelInfo"`
	Plans     []json.RawMessage `json:"plans"`
}

// Parse decodes a catalog strictly: unknown fields, wrong types and trailing
// data are errors reported with their field path.
func Parse(r io.Reader) (*Catalog, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	var ps problems
	var raw rawCatalog
	if err := strictDecode(data, &raw); err != nil {
		ps.add("", "%s", describeDecodeError(data, "", err))
		return nil, ps.err()
	}
	c := &Catalog{Currency: strings.ToUpper(strings.TrimSpace(raw.Currency))}
	c.Prices = decodeEach[PriceEntry](raw.Prices, "prices", &ps)
	c.ModelInfo = decodeEach[ModelInfoEntry](raw.ModelInfo, "modelInfo", &ps)
	c.Plans = decodeEach[PlanEntry](raw.Plans, "plans", &ps)
	if err := ps.err(); err != nil {
		return nil, err
	}
	return c, nil
}

func decodeEach[T any](items []json.RawMessage, section string, ps *problems) []T {
	if items == nil {
		return nil // section absent
	}
	out := make([]T, 0, len(items))
	for i, item := range items {
		var v T
		path := fmt.Sprintf("%s[%d]", section, i)
		if bytes.Equal(bytes.TrimSpace(item), []byte("null")) {
			ps.add(path, "不能为 null")
			continue
		}
		if err := strictDecode(item, &v); err != nil {
			ps.add(path, "%s", describeDecodeError(item, path, err))
			continue
		}
		out = append(out, v)
	}
	return out
}

func strictDecode(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("JSON 之后还有多余内容")
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("JSON 之后还有多余内容")
	}
	return nil
}

// describeDecodeError turns a json error into a message with the line and
// column (top level) or the field inside the element.
func describeDecodeError(data []byte, path string, err error) string {
	var syn *json.SyntaxError
	var typ *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syn):
		if path == "" {
			line, col := position(data, syn.Offset)
			return fmt.Sprintf("JSON 语法错误（第 %d 行第 %d 列）：%v", line, col, syn)
		}
		return fmt.Sprintf("JSON 语法错误：%v", syn)
	case errors.As(err, &typ):
		field := typ.Field
		if field == "" {
			return fmt.Sprintf("类型错误：应为 %s，实际为 %s", typ.Type, typ.Value)
		}
		return fmt.Sprintf("字段 %s 类型错误：应为 %s，实际为 %s", field, typeName(typ.Type.String()), typ.Value)
	case errors.Is(err, io.EOF):
		return "文件为空"
	}
	msg := err.Error()
	if f, ok := strings.CutPrefix(msg, "json: unknown field "); ok {
		return "未知字段 " + f
	}
	return msg
}

func typeName(t string) string {
	switch t {
	case "string", "*string":
		return "字符串"
	case "bool":
		return "布尔值"
	case "int", "int64", "*int64":
		return "整数"
	}
	return t
}

func position(data []byte, offset int64) (line, col int) {
	line, col = 1, 1
	for i := int64(0); i < offset && i < int64(len(data)); i++ {
		if data[i] == '\n' {
			line, col = line+1, 1
		} else {
			col++
		}
	}
	return line, col
}
