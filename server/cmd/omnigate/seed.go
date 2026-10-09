package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"omnigate/internal/app"
	"omnigate/internal/config"
	"omnigate/internal/platform/db"
	"omnigate/internal/seed"
)

const (
	seedUsage   = "usage: omnigate seed <file.json|-|builtin> [--dry-run]   |   omnigate seed export-builtin > catalog.json"
	exportUsage = "usage: omnigate seed export [--prices] [--model-info] [--plans] [--no-cost] > catalog.json"
)

// seedCmd runs `omnigate seed` (deploy/seed/README.md). It needs the same
// environment as `omnigate migrate` (OMNIGATE_DATABASE_URL, …), never starts
// the server and never migrates: the schema must be current.
func seedCmd(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) > 0 && args[0] == "export" {
		return seedExport(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "export-builtin" {
		if len(args) > 1 {
			return errors.New("usage: omnigate seed export-builtin > catalog.json")
		}
		_, err := stdout.Write(seed.Builtin())
		return err
	}
	fl := flag.NewFlagSet("seed", flag.ContinueOnError)
	fl.SetOutput(stderr)
	dryRun := fl.Bool("dry-run", false, "validate and print what would change; write nothing")
	pos, err := parseInterspersed(fl, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(stderr, seedUsage)
			return nil
		}
		return err
	}
	if len(pos) != 1 {
		return errors.New(seedUsage)
	}
	var data []byte
	source := "stdin"
	switch pos[0] {
	case "-":
		data, err = io.ReadAll(stdin)
	case seed.SourceBuiltin:
		data, source = seed.Builtin(), seed.SourceBuiltin
	default:
		data, err = os.ReadFile(pos[0])
		source = "file:" + pos[0]
	}
	if err != nil {
		return fmt.Errorf("seed: read catalog: %w", err)
	}
	// Parse before touching the database: syntax errors need no connection.
	catalog, err := seed.Parse(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("seed %s: %w", pos[0], err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, log, err := openForSeed(ctx, stderr, false)
	if err != nil {
		return err
	}
	defer pool.Close()
	name := pos[0]
	if source == seed.SourceBuiltin {
		name = "内置目录（sha256 " + seed.Hash(data)[:12] + "）"
	}
	if *dryRun {
		fmt.Fprintf(stdout, "试运行（--dry-run）：%s，不会写入任何数据\n", name)
	} else {
		fmt.Fprintf(stdout, "应用目录：%s\n", name)
	}
	rep, err := seed.Apply(ctx, app.SeedServices(pool, log), catalog, *dryRun)
	var verr *seed.ValidationError
	if errors.As(err, &verr) {
		return fmt.Errorf("seed %s: %w", pos[0], err)
	}
	rep.Print(stdout)
	if err != nil {
		return fmt.Errorf("seed: %w", err)
	}
	if !*dryRun {
		// Record the run: OMNIGATE_SEED_ON_START skips databases that carry
		// the marker, and the start-up log compares its hash with the catalog.
		m := seed.Marker{Source: source, SHA256: seed.Hash(data), AppliedAt: time.Now().UTC(), Trigger: "command",
			BinaryVersion: app.Version, Changes: rep.Applied()}
		if err := seed.WriteMarker(ctx, pool, m); err != nil {
			return fmt.Errorf("seed: %w", err)
		}
	}
	return nil
}

func seedExport(args []string, stdout, stderr io.Writer) error {
	fl := flag.NewFlagSet("seed export", flag.ContinueOnError)
	fl.SetOutput(stderr)
	prices := fl.Bool("prices", false, "export the effective sell and cost prices")
	info := fl.Bool("model-info", false, "export all model information")
	plans := fl.Bool("plans", false, "export the active plans")
	noCost := fl.Bool("no-cost", false, "leave out cost prices (they reference channels by name)")
	pos, err := parseInterspersed(fl, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(stderr, exportUsage)
			return nil
		}
		return err
	}
	if len(pos) > 0 {
		return errors.New(exportUsage)
	}
	opt := seed.ExportOptions{Prices: *prices, ModelInfo: *info, Plans: *plans, Cost: !*noCost}
	if !opt.Prices && !opt.ModelInfo && !opt.Plans {
		opt.Prices, opt.ModelInfo, opt.Plans = true, true, true
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, log, err := openForSeed(ctx, stderr, true)
	if err != nil {
		return err
	}
	defer pool.Close()
	c, err := seed.Export(ctx, app.SeedServices(pool, log), opt, stderr)
	if err != nil {
		return fmt.Errorf("seed export: %w", err)
	}
	if err := c.Write(stdout); err != nil {
		return err
	}
	fmt.Fprintf(stderr, "已导出：价格 %d，模型资料 %d，套餐 %d（币种 %s）\n", len(c.Prices), len(c.ModelInfo), len(c.Plans), c.Currency)
	return nil
}

// openForSeed loads the configuration (like `omnigate migrate`) and opens the
// database; logs go to stderr so stdout stays machine-readable. readOnly opens
// a connection that cannot write (PostgreSQL: read-only transactions; SQLite:
// query_only).
func openForSeed(ctx context.Context, stderr io.Writer, readOnly bool) (*db.DB, *slog.Logger, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, err
	}
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	dsn := cfg.DatabaseURL
	if readOnly {
		if dsn, err = readOnlyURL(dsn); err != nil {
			return nil, nil, err
		}
	}
	pool, err := db.Open(ctx, dsn)
	if err != nil {
		return nil, nil, err
	}
	st, err := db.MigrationStatus(ctx, pool)
	switch {
	case err != nil:
		pool.Close()
		return nil, nil, fmt.Errorf("read schema version: %w", err)
	case st.TooNew:
		pool.Close()
		return nil, nil, fmt.Errorf("%w: database is at version %d, this binary supports up to %d", db.ErrSchemaTooNew, st.Current, st.Latest)
	case st.Pending > 0:
		pool.Close()
		return nil, nil, fmt.Errorf("database schema is not current (%d migration(s) pending): run `omnigate migrate` or start the server once", st.Pending)
	}
	return pool, log, nil
}

// readOnlyURL makes a database URL read-only and refuses a SQLite file that
// does not exist (opening would create it).
func readOnlyURL(raw string) (string, error) {
	if db.IsSQLiteURL(raw) {
		path := strings.TrimPrefix(strings.TrimPrefix(raw, "sqlite:"), "//")
		if i := strings.IndexByte(path, '?'); i >= 0 {
			path = path[:i]
		}
		if path != ":memory:" {
			if _, err := os.Stat(path); err != nil {
				return "", fmt.Errorf("sqlite database %q: %w", path, err)
			}
		}
		sep := "?"
		if strings.Contains(raw, "?") {
			sep = "&"
		}
		return raw + sep + "_pragma=query_only(1)", nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse database url: %w", err)
	}
	q := u.Query()
	q.Set("default_transaction_read_only", "on")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// parseInterspersed parses flags that may appear before or after the
// positional arguments (`seed file.json --dry-run`).
func parseInterspersed(fl *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fl.Parse(args); err != nil {
			return nil, err
		}
		if fl.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fl.Arg(0))
		args = fl.Args()[1:]
	}
}
