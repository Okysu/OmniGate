package transfer

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// gooseTable is goose's version table; it is never copied (both databases are
// migrated to the same version before the copy).
const gooseTable = "goose_db_version"

// kind is the logical type of a column, derived from the PostgreSQL column
// type. Values travel between the two databases in a canonical Go form per
// kind (see values.go).
type kind int

const (
	kRaw     kind = iota // SQLite → SQLite: storage values copied unchanged
	kText                // string
	kInt                 // int64
	kFloat               // float64
	kBool                // bool
	kUUID                // string, canonical lowercase form
	kTime                // time.Time (UTC)
	kDate                // time.Time (midnight UTC)
	kJSON                // string holding a JSON document
	kDecimal             // string holding a plain decimal number
	kBytes               // []byte
	kArray               // string holding a JSON array (elem gives the element kind)
)

var kindNames = map[kind]string{kRaw: "raw", kText: "text", kInt: "integer", kFloat: "float", kBool: "boolean",
	kUUID: "uuid", kTime: "timestamp", kDate: "date", kJSON: "json", kDecimal: "numeric", kBytes: "bytes", kArray: "array"}

func (k kind) String() string { return kindNames[k] }

// sqliteStorage returns the STRICT column type a kind is stored as on SQLite.
func (k kind) sqliteStorage() string {
	switch k {
	case kInt, kBool:
		return "INTEGER"
	case kFloat:
		return "REAL"
	case kBytes:
		return "BLOB"
	}
	return "TEXT"
}

type column struct {
	Name    string
	Type    string // declared type (PostgreSQL format_type, SQLite declared type)
	Kind    kind   // PostgreSQL: derived from the type; SQLite: kRaw
	Elem    kind   // element kind of arrays
	NotNull bool
	Serial  bool // PostgreSQL: identity column or nextval() default
}

type foreignKey struct {
	Name       string
	Columns    []string
	RefTable   string
	RefColumns []string
	Deferrable bool // PostgreSQL DEFERRABLE constraint
}

type table struct {
	Name        string
	Columns     []column
	PK          []string
	FKs         []foreignKey
	Partitioned bool   // PostgreSQL partitioned (parent) table
	PartKey     string // its partition key column (single-column keys only)
}

func (t *table) column(name string) (column, bool) {
	for _, c := range t.Columns {
		if c.Name == name {
			return c, true
		}
	}
	return column{}, false
}

// nullable reports whether every column of fk may hold NULL.
func (t *table) nullable(fk foreignKey) bool {
	for _, name := range fk.Columns {
		if c, ok := t.column(name); !ok || c.NotNull {
			return false
		}
	}
	return true
}

// queryFunc runs a catalog query and returns every row (small result sets).
type queryFunc func(ctx context.Context, q string, args ...any) ([][]any, error)

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

func quoteList(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = quoteIdent(n)
	}
	return strings.Join(q, ", ")
}

func str(v any) string {
	switch s := v.(type) {
	case nil:
		return ""
	case string:
		return s
	case []byte:
		return string(s)
	}
	return fmt.Sprint(v)
}

func truthy(v any) bool {
	switch b := v.(type) {
	case bool:
		return b
	case int64:
		return b != 0
	case int32:
		return b != 0
	}
	return false
}

func toInt(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int32:
		return int64(n)
	case int16:
		return int64(n)
	case int:
		return int64(n)
	case float64:
		return int64(n)
	}
	return 0
}

// describeSQLite lists the tables of a SQLite database (sqlite_master,
// pragma_table_xinfo, pragma_foreign_key_list), goose's table excluded.
func describeSQLite(ctx context.Context, q queryFunc) ([]*table, error) {
	rows, err := q(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite\_%' ESCAPE '\' ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list sqlite tables: %w", err)
	}
	var out []*table
	for _, r := range rows {
		name := str(r[0])
		if name == gooseTable {
			continue
		}
		t := &table{Name: name}
		cols, err := q(ctx, `SELECT name, type, "notnull", pk, hidden FROM pragma_table_xinfo(?1) ORDER BY cid`, name)
		if err != nil {
			return nil, fmt.Errorf("describe %s: %w", name, err)
		}
		type pkCol struct {
			pos  int64
			name string
		}
		var pks []pkCol
		for _, c := range cols {
			if toInt(c[4]) != 0 {
				continue // hidden or generated column
			}
			t.Columns = append(t.Columns, column{Name: str(c[0]), Type: strings.ToUpper(str(c[1])), Kind: kRaw, NotNull: truthy(c[2])})
			if p := toInt(c[3]); p > 0 {
				pks = append(pks, pkCol{p, str(c[0])})
			}
		}
		slices.SortFunc(pks, func(a, b pkCol) int { return int(a.pos - b.pos) })
		for _, p := range pks {
			t.PK = append(t.PK, p.name)
		}
		fks, err := q(ctx, `SELECT id, "table", "from", "to" FROM pragma_foreign_key_list(?1) ORDER BY id, seq`, name)
		if err != nil {
			return nil, fmt.Errorf("foreign keys of %s: %w", name, err)
		}
		byID := map[int64]*foreignKey{}
		var ids []int64
		for _, f := range fks {
			id := toInt(f[0])
			fk := byID[id]
			if fk == nil {
				fk = &foreignKey{Name: fmt.Sprintf("%s_fk%d", name, id), RefTable: str(f[1])}
				byID[id] = fk
				ids = append(ids, id)
			}
			fk.Columns = append(fk.Columns, str(f[2]))
			fk.RefColumns = append(fk.RefColumns, str(f[3])) // "" = the parent's primary key
		}
		for _, id := range ids {
			t.FKs = append(t.FKs, *byID[id])
		}
		out = append(out, t)
	}
	return out, nil
}

// describePostgres lists the ordinary and partitioned tables of the current
// schema from pg_catalog (partitions themselves and goose's table excluded).
func describePostgres(ctx context.Context, q queryFunc) ([]*table, error) {
	rows, err := q(ctx, `SELECT c.oid::bigint, c.relname::text, c.relkind = 'p',
			CASE WHEN c.relkind = 'p' THEN pg_get_partkeydef(c.oid) ELSE '' END
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = current_schema() AND c.relkind IN ('r', 'p') AND NOT c.relispartition
		ORDER BY c.relname`)
	if err != nil {
		return nil, fmt.Errorf("list postgres tables: %w", err)
	}
	var out []*table
	for _, r := range rows {
		oid, name := toInt(r[0]), str(r[1])
		if name == gooseTable {
			continue
		}
		t := &table{Name: name, Partitioned: truthy(r[2])}
		if t.Partitioned {
			t.PartKey = partitionColumn(str(r[3]))
		}
		cols, err := q(ctx, `SELECT a.attname::text, format_type(a.atttypid, a.atttypmod),
				CASE WHEN t.typtype = 'd' THEN bt.typname ELSE t.typname END::text,
				CASE WHEN t.typtype = 'd' THEN bt.typcategory ELSE t.typcategory END::text,
				coalesce(et.typname::text, ''), t.typtype::text, a.attnotnull,
				a.attidentity <> '' OR coalesce(pg_get_expr(d.adbin, d.adrelid), '') LIKE 'nextval(%',
				a.attgenerated <> ''
			FROM pg_attribute a
			JOIN pg_type t ON t.oid = a.atttypid
			LEFT JOIN pg_type bt ON bt.oid = t.typbasetype
			LEFT JOIN pg_type et ON et.oid = (CASE WHEN t.typtype = 'd' THEN bt.typelem ELSE t.typelem END)
			LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
			WHERE a.attrelid = $1::bigint::oid AND a.attnum > 0 AND NOT a.attisdropped
			ORDER BY a.attnum`, oid)
		if err != nil {
			return nil, fmt.Errorf("describe %s: %w", name, err)
		}
		for _, c := range cols {
			if truthy(c[8]) {
				continue // generated column: computed by the target
			}
			col := column{Name: str(c[0]), Type: str(c[1]), NotNull: truthy(c[6]), Serial: truthy(c[7])}
			if str(c[5]) == "e" {
				return nil, fmt.Errorf("column %s.%s: enum type %s is not supported", name, col.Name, col.Type)
			}
			col.Kind, col.Elem, err = pgKind(str(c[2]), str(c[3]), str(c[4]))
			if err != nil {
				return nil, fmt.Errorf("column %s.%s: %w", name, col.Name, err)
			}
			t.Columns = append(t.Columns, col)
		}
		pk, err := q(ctx, `SELECT a.attname::text FROM pg_index i
			JOIN LATERAL unnest(i.indkey) WITH ORDINALITY k(attnum, ord) ON true
			JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum
			WHERE i.indrelid = $1::bigint::oid AND i.indisprimary ORDER BY k.ord`, oid)
		if err != nil {
			return nil, fmt.Errorf("primary key of %s: %w", name, err)
		}
		for _, p := range pk {
			t.PK = append(t.PK, str(p[0]))
		}
		fks, err := q(ctx, `SELECT con.conname::text, rc.relname::text, con.condeferrable,
				array_to_string(ARRAY(SELECT a.attname::text FROM unnest(con.conkey) WITH ORDINALITY k(n, o)
					JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.n ORDER BY k.o), chr(1)),
				array_to_string(ARRAY(SELECT a.attname::text FROM unnest(con.confkey) WITH ORDINALITY k(n, o)
					JOIN pg_attribute a ON a.attrelid = con.confrelid AND a.attnum = k.n ORDER BY k.o), chr(1))
			FROM pg_constraint con JOIN pg_class rc ON rc.oid = con.confrelid
			WHERE con.contype = 'f' AND con.conrelid = $1::bigint::oid AND con.conparentid = 0
			ORDER BY con.conname`, oid)
		if err != nil {
			return nil, fmt.Errorf("foreign keys of %s: %w", name, err)
		}
		for _, f := range fks {
			t.FKs = append(t.FKs, foreignKey{Name: str(f[0]), RefTable: str(f[1]), Deferrable: truthy(f[2]),
				Columns: strings.Split(str(f[3]), "\x01"), RefColumns: strings.Split(str(f[4]), "\x01")})
		}
		out = append(out, t)
	}
	return out, nil
}

// partitionColumn extracts the column of a single-column partition key such
// as "RANGE (started_at)" ("" for expressions or multi-column keys).
func partitionColumn(def string) string {
	i, j := strings.IndexByte(def, '('), strings.LastIndexByte(def, ')')
	if i < 0 || j < i {
		return ""
	}
	col := strings.TrimSpace(def[i+1 : j])
	if strings.ContainsAny(col, ",() ") {
		return ""
	}
	return strings.Trim(col, `"`)
}

// pgKind maps a PostgreSQL type (typname, typcategory, element typname) to a
// kind. Unsupported types are rejected up front rather than mangled.
func pgKind(typ, category, elem string) (kind, kind, error) {
	switch typ {
	case "uuid":
		return kUUID, 0, nil
	case "timestamptz", "timestamp":
		return kTime, 0, nil
	case "date":
		return kDate, 0, nil
	case "json", "jsonb":
		return kJSON, 0, nil
	case "numeric":
		return kDecimal, 0, nil
	case "bytea":
		return kBytes, 0, nil
	case "bool":
		return kBool, 0, nil
	case "int2", "int4", "int8":
		return kInt, 0, nil
	case "float4", "float8":
		return kFloat, 0, nil
	case "text", "varchar", "bpchar", "name", "citext":
		return kText, 0, nil
	}
	if category == "A" {
		ek, _, err := pgKind(elem, "", "")
		if err != nil {
			return 0, 0, fmt.Errorf("unsupported array type %s[]", elem)
		}
		switch ek {
		case kText, kInt, kFloat, kBool:
			return kArray, ek, nil
		}
		return 0, 0, fmt.Errorf("unsupported array type %s[]", elem)
	}
	return 0, 0, fmt.Errorf("unsupported column type %s", typ)
}
