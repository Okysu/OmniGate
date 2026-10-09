package seed

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"omnigate/internal/platform/db"
)

// builtinCatalog is the catalog shipped in the binary (and so in the image):
// GPT sell prices, model information and the six subscription plans, in USD.
// It is the single source of truth; `omnigate seed export-builtin` prints it.
//
//go:embed builtin/catalog.json
var builtinCatalog []byte

// SourceBuiltin names the embedded catalog (`omnigate seed builtin`,
// OMNIGATE_SEED_ON_START=builtin).
const SourceBuiltin = "builtin"

// Builtin returns a copy of the embedded catalog file.
func Builtin() []byte { return bytes.Clone(builtinCatalog) }

// Hash identifies a catalog file's content (hex SHA-256 of the bytes).
func Hash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// MarkerKey is the system_settings row recording the last successful seed run.
const MarkerKey = "seed.applied"

// Marker is the value of the seed.applied setting. Its presence makes
// OMNIGATE_SEED_ON_START a no-op: the start-up seed runs once per database.
type Marker struct {
	Source        string    `json:"source"` // "builtin", "file:<path>" or "stdin"
	SHA256        string    `json:"sha256"`
	AppliedAt     time.Time `json:"appliedAt"`
	Trigger       string    `json:"trigger"` // "startup" or "command"
	BinaryVersion string    `json:"binaryVersion,omitempty"`
	Changes       int       `json:"changes"` // entries written (0: already identical)
}

// ReadMarker returns the seed.applied marker, nil when the database was never
// seeded (or only seeded before markers existed).
func ReadMarker(ctx context.Context, pool *db.DB) (*Marker, error) {
	var m Marker
	err := pool.QueryRow(ctx, `SELECT value FROM system_settings WHERE key = $1`, MarkerKey).Scan(&m)
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", MarkerKey, err)
	}
	return &m, nil
}

// WriteMarker records a successful (non dry-run) seed run.
func WriteMarker(ctx context.Context, pool *db.DB, m Marker) error {
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, `INSERT INTO system_settings (key, value, version, updated_at) VALUES ($1, $2, 1, $3)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, version = system_settings.version + 1, updated_at = excluded.updated_at`,
		MarkerKey, raw, m.AppliedAt)
	if err != nil {
		return fmt.Errorf("write %s: %w", MarkerKey, err)
	}
	return nil
}

// LoadSource reads the catalog named by an OMNIGATE_SEED_ON_START value or
// a `seed` argument: "builtin" is the embedded catalog, anything else a file
// path. It returns the raw bytes and the marker source name.
func LoadSource(spec string) ([]byte, string, error) {
	if spec == SourceBuiltin {
		return Builtin(), SourceBuiltin, nil
	}
	data, err := os.ReadFile(spec)
	if err != nil {
		return nil, "", fmt.Errorf("read catalog: %w", err)
	}
	return data, "file:" + spec, nil
}

// OnStartOutcome is what OnStart did.
type OnStartOutcome string

const (
	OnStartDisabled        OnStartOutcome = "disabled"
	OnStartApplied         OnStartOutcome = "applied"          // written (or verified identical) and marked
	OnStartSkippedMarker   OnStartOutcome = "skipped-marker"   // already seeded once
	OnStartSkippedCurrency OnStartOutcome = "skipped-currency" // catalog currency ≠ settlement currency
	OnStartFailed          OnStartOutcome = "failed"           // unreadable/invalid catalog or write error
)

// OnStart applies the catalog named by spec (OMNIGATE_SEED_ON_START) once per
// database. Call it after migrations and after the settlement currency is
// stored. It never fails the start: problems are logged and the server keeps
// starting without seeding.
//
//   - a seed.applied marker exists → skip (and log when the built-in catalog
//     changed since, with the manual command to apply it);
//   - the catalog's currency differs from the stored one → log an error, skip;
//   - otherwise apply (idempotent: identical data writes nothing) and record
//     the marker.
func OnStart(ctx context.Context, svc Services, spec, version string, log *slog.Logger) OnStartOutcome {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return OnStartDisabled
	}
	log = log.With("component", "seed", "source", spec)
	data, source, err := LoadSource(spec)
	if err != nil {
		log.Error("OMNIGATE_SEED_ON_START: cannot read the catalog; starting without seeding", "err", err)
		return OnStartFailed
	}
	hash := Hash(data)
	marker, err := ReadMarker(ctx, svc.Pool)
	if err != nil {
		log.Error("OMNIGATE_SEED_ON_START: cannot read the seed marker; starting without seeding", "err", err)
		return OnStartFailed
	}
	if marker != nil {
		if marker.Source == source && marker.SHA256 != hash {
			log.Info("catalog seeding skipped: this database was already seeded once; the catalog has changed since. "+
				"Review with `omnigate seed "+spec+" --dry-run`, then apply with `omnigate seed "+spec+"`",
				"applied_at", marker.AppliedAt, "applied_sha256", short12(marker.SHA256), "current_sha256", short12(hash))
		} else {
			log.Info("catalog seeding skipped: this database was already seeded once",
				"applied_source", marker.Source, "applied_at", marker.AppliedAt, "applied_sha256", short12(marker.SHA256))
		}
		return OnStartSkippedMarker
	}
	catalog, err := Parse(bytes.NewReader(data))
	if err != nil {
		log.Error("OMNIGATE_SEED_ON_START: the catalog is invalid; starting without seeding", "err", err)
		return OnStartFailed
	}
	if catalog.Currency != "" {
		stored, err := StoredCurrency(ctx, svc.Pool)
		if err != nil {
			log.Error("OMNIGATE_SEED_ON_START: cannot read the settlement currency; starting without seeding", "err", err)
			return OnStartFailed
		}
		if stored != catalog.Currency {
			log.Error("OMNIGATE_SEED_ON_START: catalog currency differs from the settlement currency; amounts are never converted, "+
				"so nothing was seeded. Unset OMNIGATE_SEED_ON_START or seed a catalog in the deployment's currency",
				"catalog_currency", catalog.Currency, "settlement_currency", stored)
			return OnStartSkippedCurrency
		}
	}
	rep, err := Apply(ctx, svc, catalog, false)
	if err != nil {
		applied := 0
		if rep != nil {
			applied = rep.Applied()
		}
		log.Error("OMNIGATE_SEED_ON_START: seeding failed; it will be retried on the next start", "err", err, "applied", applied)
		return OnStartFailed
	}
	m := Marker{Source: source, SHA256: hash, AppliedAt: time.Now().UTC(), Trigger: "startup", BinaryVersion: version, Changes: rep.Applied()}
	if err := WriteMarker(ctx, svc.Pool, m); err != nil {
		log.Error("OMNIGATE_SEED_ON_START: catalog applied but the marker could not be written; the next start re-checks (idempotent)", "err", err)
		return OnStartFailed
	}
	log.Info("catalog seeded on first start", "written", rep.Applied(), "unchanged", len(rep.Changes)-rep.Pending(),
		"request_id", rep.RequestID, "sha256", short12(hash), "summary", rep.Summary())
	return OnStartApplied
}

// Summary is the one-line per-section count ("价格：新建 8；…").
func (r *Report) Summary() string {
	counts := r.Counts()
	parts := []string{}
	for _, sec := range []string{"price", "model-info", "plan"} {
		c := counts[sec]
		if c == nil {
			continue
		}
		items := []string{}
		for _, a := range []Action{ActionCreate, ActionNewVersion, ActionUpdate, ActionUnchanged} {
			if c[a] > 0 {
				items = append(items, fmt.Sprintf("%s %d", actionText[a], c[a]))
			}
		}
		parts = append(parts, sectionText[sec]+"："+strings.Join(items, "，"))
	}
	if len(parts) == 0 {
		return "目录为空"
	}
	return strings.Join(parts, "；")
}

func short12(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}
