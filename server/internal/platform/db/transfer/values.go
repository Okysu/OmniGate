package transfer

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"omnigate/internal/platform/db"
)

// Values travel from source to target in a canonical Go form per kind:
//
//	kText, kJSON, kDecimal, kArray, kUUID → string
//	kInt → int64, kFloat → float64, kBool → bool, kBytes → []byte
//	kTime, kDate → time.Time (UTC)
//	kRaw → the SQLite storage value, unchanged
//
// NULL is nil throughout. from* functions build the canonical form from a
// driver value, to* functions encode it for the target driver.

// pgSelectExpr returns the SELECT expression reading column c on PostgreSQL:
// types without a stable Go representation are read as text.
func pgSelectExpr(c column) string {
	q := quoteIdent(c.Name)
	switch c.Kind {
	case kUUID, kJSON, kDecimal, kText:
		return q + "::text"
	case kArray:
		return "to_json(" + q + ")::text"
	}
	return q
}

// fromPG converts a value scanned by pgx (with pgSelectExpr) to canonical form.
func fromPG(v any, k kind) (any, error) {
	if v == nil {
		return nil, nil
	}
	switch k {
	case kInt:
		switch n := v.(type) {
		case int64:
			return n, nil
		case int32:
			return int64(n), nil
		case int16:
			return int64(n), nil
		}
	case kFloat:
		switch f := v.(type) {
		case float64:
			return f, nil
		case float32:
			return float64(f), nil
		}
	case kBool:
		if b, ok := v.(bool); ok {
			return b, nil
		}
	case kTime, kDate:
		if t, ok := v.(time.Time); ok {
			return t.UTC(), nil
		}
	case kBytes:
		if b, ok := v.([]byte); ok {
			return b, nil
		}
	case kText, kUUID, kJSON, kDecimal, kArray:
		if s, ok := v.(string); ok {
			return s, nil
		}
	}
	return nil, fmt.Errorf("unexpected %T for a %s column", v, k)
}

// fromSQLite converts a SQLite storage value (int64, float64, string, []byte)
// to canonical form.
func fromSQLite(v any, k kind) (any, error) {
	if v == nil || k == kRaw {
		return v, nil
	}
	switch k {
	case kText, kJSON, kArray:
		switch s := v.(type) {
		case string:
			return s, nil
		case []byte:
			return string(s), nil
		}
	case kDecimal:
		switch s := v.(type) {
		case string:
			return s, nil
		case int64:
			return strconv.FormatInt(s, 10), nil
		case float64:
			return strconv.FormatFloat(s, 'f', -1, 64), nil
		}
	case kUUID:
		switch s := v.(type) {
		case string:
			u, err := uuid.Parse(s)
			if err != nil {
				return nil, fmt.Errorf("invalid uuid %q", s)
			}
			return u.String(), nil
		case []byte:
			if u, err := uuid.FromBytes(s); err == nil {
				return u.String(), nil
			}
			if u, err := uuid.ParseBytes(s); err == nil {
				return u.String(), nil
			}
		}
	case kInt:
		switch n := v.(type) {
		case int64:
			return n, nil
		case float64:
			if n == math.Trunc(n) && math.Abs(n) < 1<<63 {
				return int64(n), nil
			}
		case string:
			return strconv.ParseInt(n, 10, 64)
		}
	case kFloat:
		switch f := v.(type) {
		case float64:
			return f, nil
		case int64:
			return float64(f), nil
		case string:
			return strconv.ParseFloat(f, 64)
		}
	case kBool:
		switch b := v.(type) {
		case int64:
			return b != 0, nil
		case bool:
			return b, nil
		case string:
			return strconv.ParseBool(b)
		}
	case kTime:
		return parseTime(v)
	case kDate:
		t, err := parseTime(v)
		if err != nil {
			return nil, err
		}
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), nil
	case kBytes:
		switch b := v.(type) {
		case []byte:
			return b, nil
		case string:
			return []byte(b), nil
		}
	}
	return nil, fmt.Errorf("unexpected %T for a %s column", v, k)
}

// parseTime parses SQLite timestamp text: the canonical format, any RFC 3339
// variant, SQLite's own "YYYY-MM-DD HH:MM:SS[.SSS]" or a bare date.
func parseTime(v any) (time.Time, error) {
	var s string
	switch t := v.(type) {
	case time.Time:
		return t.UTC(), nil
	case string:
		s = t
	case []byte:
		s = string(t)
	default:
		return time.Time{}, fmt.Errorf("unexpected %T for a timestamp column", v)
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC(), nil
	}
	for _, layout := range []string{"2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05.999999999",
		"2006-01-02T15:04:05.999999999", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse %q as a timestamp", s)
}

// toSQLite encodes a canonical value as the SQLite storage value the
// application itself writes for the kind (ADR-0009): canonical timestamp
// text, 0/1 for booleans, JSON and decimals as text, bytes as BLOB.
func toSQLite(v any, k kind) (any, error) {
	if v == nil {
		return nil, nil
	}
	switch k {
	case kTime:
		return db.TimeText(v.(time.Time)), nil
	case kDate:
		return v.(time.Time).UTC().Format(time.DateOnly), nil
	case kBool:
		if v.(bool) {
			return int64(1), nil
		}
		return int64(0), nil
	case kDecimal:
		return normalizeDecimal(v.(string)), nil
	}
	if b, ok := v.([]byte); ok && b == nil {
		return []byte{}, nil // an empty BLOB, not NULL
	}
	return v, nil
}

// normalizeDecimal trims trailing fractional zeros ("12.500000000" → "12.5"),
// the form the application writes for numeric(38,9) values on SQLite.
func normalizeDecimal(s string) string {
	if !strings.Contains(s, ".") || strings.ContainsAny(s, "eE") {
		return s
	}
	s = strings.TrimRight(s, "0")
	s = strings.TrimSuffix(s, ".")
	if s == "-0" || s == "" || s == "-" {
		return "0"
	}
	return s
}

// toPG encodes a canonical value for pgx's binary COPY protocol.
func toPG(v any, c column) (any, error) {
	if v == nil {
		return nil, nil
	}
	switch c.Kind {
	case kUUID:
		u, err := uuid.Parse(v.(string))
		if err != nil {
			return nil, fmt.Errorf("invalid uuid %q", v)
		}
		return u, nil
	case kDecimal:
		var n pgtype.Numeric
		if err := n.Scan(v.(string)); err != nil {
			return nil, fmt.Errorf("invalid decimal %q: %w", v, err)
		}
		return n, nil
	case kJSON:
		s := v.(string)
		if !json.Valid([]byte(s)) {
			return nil, fmt.Errorf("invalid JSON document %.40q", s)
		}
		return s, nil
	case kArray:
		return decodeArray(v.(string), c.Elem)
	case kBytes:
		if b := v.([]byte); b == nil {
			return []byte{}, nil
		}
	}
	return v, nil
}

func decodeArray(s string, elem kind) (any, error) {
	if s == "null" {
		return nil, nil
	}
	switch elem {
	case kText:
		return unmarshalArray[string](s)
	case kInt:
		return unmarshalArray[int64](s)
	case kFloat:
		return unmarshalArray[float64](s)
	case kBool:
		return unmarshalArray[bool](s)
	}
	return nil, fmt.Errorf("unsupported array element %s", elem)
}

// unmarshalArray decodes a JSON array (elements may be null).
func unmarshalArray[T any](s string) (any, error) {
	var a []*T
	if err := json.Unmarshal([]byte(s), &a); err != nil {
		return nil, fmt.Errorf("invalid array %.40q: %w", s, err)
	}
	return a, nil
}
