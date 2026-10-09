package db

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"time"
	"unicode/utf8"
)

// sqliteArgs converts query arguments to SQLite storage values, mirroring how
// pgx encodes them for the PostgreSQL column types used by the schema:
//
//   - time.Time → canonical UTC text (TimeText), so text order is time order;
//   - driver.Valuer (uuid.UUID, …) → its value;
//   - nil pointers, slices and maps → NULL; other pointers are dereferenced;
//   - integers, floats, bools and strings (including named types) → themselves;
//   - []byte / json.RawMessage holding valid UTF-8 JSON → text (jsonb
//     columns), any other []byte → blob (bytea columns);
//   - other slices, arrays, maps and structs → JSON text (jsonb columns and
//     the JSON arrays that "= ANY($N)" expands with json_each).
func sqliteArgs(args []any) ([]any, error) {
	out := make([]any, len(args))
	for i, a := range args {
		v, err := sqliteValue(a)
		if err != nil {
			return nil, fmt.Errorf("db: argument $%d: %w", i+1, err)
		}
		out[i] = v
	}
	return out, nil
}

var (
	valuerType = reflect.TypeFor[driver.Valuer]()
	timeType   = reflect.TypeFor[time.Time]()
)

func sqliteValue(a any) (any, error) {
	switch v := a.(type) {
	case nil:
		return nil, nil
	case string, int64, float64, bool:
		return v, nil
	case int:
		return int64(v), nil
	case int32:
		return int64(v), nil
	case time.Time:
		return TimeText(v), nil
	case []byte:
		return bytesValue(v), nil
	case json.RawMessage:
		if v == nil {
			return nil, nil
		}
		return string(v), nil
	}
	rv := reflect.ValueOf(a)
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil, nil
		}
		if rv.Type().Implements(valuerType) && !rv.Type().Elem().Implements(valuerType) {
			return valuerValue(a.(driver.Valuer))
		}
		return sqliteValue(rv.Elem().Interface())
	}
	if vr, ok := a.(driver.Valuer); ok {
		return valuerValue(vr)
	}
	switch rv.Kind() {
	case reflect.Bool:
		return rv.Bool(), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int(), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		u := rv.Uint()
		if u > math.MaxInt64 {
			return nil, fmt.Errorf("uint64 %d overflows int64", u)
		}
		return int64(u), nil
	case reflect.Float32, reflect.Float64:
		return rv.Float(), nil
	case reflect.String:
		return rv.String(), nil
	case reflect.Slice:
		if rv.IsNil() {
			return nil, nil
		}
		if rv.Type().Elem().Kind() == reflect.Uint8 {
			return bytesValue(rv.Bytes()), nil
		}
	case reflect.Map:
		if rv.IsNil() {
			return nil, nil
		}
	case reflect.Struct:
		if rv.Type().ConvertibleTo(timeType) {
			return TimeText(rv.Convert(timeType).Interface().(time.Time)), nil
		}
	case reflect.Array:
	default:
		return nil, fmt.Errorf("unsupported type %T", a)
	}
	b, err := json.Marshal(a)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

func valuerValue(vr driver.Valuer) (any, error) {
	v, err := vr.Value()
	if err != nil {
		return nil, err
	}
	if t, ok := v.(time.Time); ok {
		return TimeText(t), nil
	}
	return v, nil
}

// bytesValue stores JSON documents as text and everything else as a blob.
func bytesValue(b []byte) any {
	if b == nil {
		return nil
	}
	if len(b) > 0 && utf8.Valid(b) && json.Valid(b) {
		return string(b)
	}
	return append([]byte(nil), b...)
}

// assign stores the SQLite value src (nil, int64, float64, string or []byte)
// in dest the way pgx would scan the corresponding PostgreSQL value.
func assign(dest, src any) error {
	switch d := dest.(type) {
	case nil:
		return nil // pgx allows nil destinations to skip a column
	case *any:
		if b, ok := src.([]byte); ok {
			src = append([]byte(nil), b...)
		}
		*d = src
		return nil
	case *string:
		if src == nil {
			return fmt.Errorf("cannot scan NULL into %T", dest)
		}
		s, err := asString(src)
		*d = s
		return err
	case *[]byte:
		return assignBytes(d, src)
	case *json.RawMessage:
		return assignBytes((*[]byte)(d), src)
	case *time.Time:
		if src == nil {
			return fmt.Errorf("cannot scan NULL into %T", dest)
		}
		t, err := asTime(src)
		*d = t
		return err
	case *bool:
		if src == nil {
			return fmt.Errorf("cannot scan NULL into %T", dest)
		}
		b, err := asBool(src)
		*d = b
		return err
	case *int64:
		if src == nil {
			return fmt.Errorf("cannot scan NULL into %T", dest)
		}
		n, err := asInt(src)
		*d = n
		return err
	case *int:
		if src == nil {
			return fmt.Errorf("cannot scan NULL into %T", dest)
		}
		n, err := asInt(src)
		*d = int(n)
		return err
	case sql.Scanner:
		return d.Scan(src)
	}

	dv := reflect.ValueOf(dest)
	if dv.Kind() != reflect.Pointer || dv.IsNil() {
		return fmt.Errorf("destination %T is not a non-nil pointer", dest)
	}
	ev := dv.Elem()
	if src == nil {
		switch ev.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Map, reflect.Interface:
			ev.SetZero()
			return nil
		}
		return fmt.Errorf("cannot scan NULL into %T", dest)
	}
	if ev.Kind() == reflect.Pointer {
		nv := reflect.New(ev.Type().Elem())
		if err := assign(nv.Interface(), src); err != nil {
			return err
		}
		ev.Set(nv)
		return nil
	}
	switch ev.Kind() {
	case reflect.Bool:
		b, err := asBool(src)
		if err != nil {
			return err
		}
		ev.SetBool(b)
		return nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := asInt(src)
		if err != nil {
			return err
		}
		if ev.OverflowInt(n) {
			return fmt.Errorf("value %d overflows %s", n, ev.Type())
		}
		ev.SetInt(n)
		return nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := asInt(src)
		if err != nil {
			return err
		}
		if n < 0 || ev.OverflowUint(uint64(n)) {
			return fmt.Errorf("value %d overflows %s", n, ev.Type())
		}
		ev.SetUint(uint64(n))
		return nil
	case reflect.Float32, reflect.Float64:
		f, err := asFloat(src)
		if err != nil {
			return err
		}
		ev.SetFloat(f)
		return nil
	case reflect.String:
		s, err := asString(src)
		if err != nil {
			return err
		}
		ev.SetString(s)
		return nil
	case reflect.Struct, reflect.Slice, reflect.Map, reflect.Array:
		if ev.Type().ConvertibleTo(timeType) {
			t, err := asTime(src)
			if err != nil {
				return err
			}
			ev.Set(reflect.ValueOf(t).Convert(ev.Type()))
			return nil
		}
		var raw []byte
		switch s := src.(type) {
		case string:
			raw = []byte(s)
		case []byte:
			raw = s
		default:
			return fmt.Errorf("cannot scan %T into %T", src, dest)
		}
		if err := json.Unmarshal(raw, dest); err != nil {
			return fmt.Errorf("decode JSON into %T: %w", dest, err)
		}
		return nil
	}
	return fmt.Errorf("cannot scan %T into %T", src, dest)
}

func assignBytes(d *[]byte, src any) error {
	switch s := src.(type) {
	case nil:
		*d = nil
	case []byte:
		*d = append([]byte(nil), s...)
	case string:
		*d = []byte(s)
	default:
		return fmt.Errorf("cannot scan %T into []byte", src)
	}
	return nil
}

func asString(src any) (string, error) {
	switch s := src.(type) {
	case string:
		return s, nil
	case []byte:
		return string(s), nil
	case int64:
		return strconv.FormatInt(s, 10), nil
	case float64:
		return strconv.FormatFloat(s, 'f', -1, 64), nil
	}
	return "", fmt.Errorf("cannot scan %T into a string", src)
}

func asInt(src any) (int64, error) {
	switch s := src.(type) {
	case int64:
		return s, nil
	case float64:
		if s != math.Trunc(s) || s > math.MaxInt64 || s < math.MinInt64 {
			return 0, fmt.Errorf("cannot scan non-integer %v into an integer", s)
		}
		return int64(s), nil
	case string:
		return strconv.ParseInt(s, 10, 64)
	case []byte:
		return strconv.ParseInt(string(s), 10, 64)
	}
	return 0, fmt.Errorf("cannot scan %T into an integer", src)
}

func asFloat(src any) (float64, error) {
	switch s := src.(type) {
	case float64:
		return s, nil
	case int64:
		return float64(s), nil
	case string:
		return strconv.ParseFloat(s, 64)
	}
	return 0, fmt.Errorf("cannot scan %T into a float", src)
}

func asBool(src any) (bool, error) {
	switch s := src.(type) {
	case int64:
		return s != 0, nil
	case bool:
		return s, nil
	case string:
		return strconv.ParseBool(s)
	}
	return false, fmt.Errorf("cannot scan %T into a bool", src)
}

// asTime parses SQLite timestamp text: the canonical format, any RFC 3339
// variant, or SQLite's own "YYYY-MM-DD HH:MM:SS[.SSS]" (CURRENT_TIMESTAMP).
func asTime(src any) (time.Time, error) {
	var s string
	switch v := src.(type) {
	case string:
		s = v
	case []byte:
		s = string(v)
	case time.Time:
		return v.UTC(), nil
	default:
		return time.Time{}, fmt.Errorf("cannot scan %T into a time", src)
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC(), nil
	}
	for _, layout := range []string{"2006-01-02 15:04:05.999999999", "2006-01-02T15:04:05.999999999", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse %q as a timestamp", s)
}
