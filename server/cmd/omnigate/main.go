// Command omnigate runs the OmniGate AI gateway.
//
//	omnigate [serve]          run migrations (unless OMNIGATE_AUTO_MIGRATE=false) and serve
//	omnigate migrate          apply pending migrations and exit
//	omnigate migrate status   print the schema version
//	omnigate backup <file>    SQLite only: write a consistent copy of the database
//	                          (VACUUM INTO; safe while the server runs)
//	omnigate migrate-db --from <url> --to <url> [--batch 1000] [--dry-run] [--force-empty-check=false]
//	                          copy all data between databases (SQLite ⇄ PostgreSQL);
//	                          stop the server first
//	omnigate seed <file.json|-|builtin> [--dry-run]
//	                          apply a declarative catalog (prices, model info, plans)
//	                          through the service layer; idempotent (deploy/seed/README.md).
//	                          "builtin" is the catalog embedded in the binary
//	omnigate seed export-builtin
//	                          print the embedded catalog (no database needed)
//	omnigate seed export [--prices] [--model-info] [--plans] [--no-cost]
//	                          print the current catalog in the same format (read-only)
//	omnigate keygen           print a new master key entry for OMNIGATE_MASTER_KEY
//	omnigate healthcheck      probe /healthz on OMNIGATE_HTTP_ADDR (exit 0 when healthy);
//	                          for container HEALTHCHECKs in images without curl/wget
//	omnigate version          print the version (and commit / build time when known)
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	// Embedded IANA zone database: calendar quota windows use time zones and
	// minimal container images have no /usr/share/zoneinfo.
	_ "time/tzdata"

	"omnigate/internal/app"
	"omnigate/internal/config"
	"omnigate/internal/platform/db"
	"omnigate/internal/platform/db/transfer"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "omnigate:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cmd := "serve"
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "keygen":
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return err
		}
		suffix := make([]byte, 3)
		if _, err := rand.Read(suffix); err != nil {
			return err
		}
		fmt.Printf("k%s-%x:%s\n", time.Now().UTC().Format("20060102"), suffix, base64.StdEncoding.EncodeToString(raw))
		return nil
	case "version":
		fmt.Println(app.Version)
		if app.Commit != "" || app.BuildTime != "" {
			fmt.Printf("commit %s, built %s\n", app.Commit, app.BuildTime)
		}
		return nil
	case "healthcheck":
		return healthcheck(os.Getenv("OMNIGATE_HTTP_ADDR"))
	case "migrate-db":
		return migrateDB(args[1:])
	case "seed":
		return seedCmd(args[1:], os.Stdin, os.Stdout, os.Stderr)
	case "serve", "migrate":
	case "backup":
		if len(args) != 2 {
			return fmt.Errorf("usage: omnigate backup <file>")
		}
	default:
		return fmt.Errorf("unknown command %q", cmd)
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := newLogger(cfg)
	slog.SetDefault(log)
	for _, w := range cfg.Warnings {
		log.Warn("config: " + w)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if cmd == "backup" {
		if err := db.Backup(ctx, pool, args[1]); err != nil {
			return err
		}
		fmt.Println("backup written to", args[1])
		return nil
	}
	if cmd == "migrate" {
		if len(args) > 1 && args[1] == "status" {
			st, err := db.MigrationStatus(ctx, pool)
			if err != nil {
				return err
			}
			return json.NewEncoder(os.Stdout).Encode(st)
		}
		return db.Migrate(ctx, pool, log)
	}

	if pool.Dialect() == db.SQLite {
		log.Info("database: SQLite (single instance only)", "path", pool.SQLPath())
	} else {
		log.Info("database: PostgreSQL")
	}
	if cfg.AutoMigrate {
		if err := db.Migrate(ctx, pool, log); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	} else if err := db.CheckSchemaVersion(ctx, pool); err != nil {
		return err
	}
	a, err := app.New(ctx, cfg, log, pool, app.Options{})
	if err != nil {
		return err
	}
	return a.Run(ctx)
}

// migrateDB runs `omnigate migrate-db` (docs/operations/deployment.md).
func migrateDB(args []string) error {
	fl := flag.NewFlagSet("migrate-db", flag.ContinueOnError)
	from := fl.String("from", "", "source database URL (sqlite:///path.db or postgres://…)")
	to := fl.String("to", "", "target database URL (must be empty)")
	batch := fl.Int("batch", transfer.DefaultBatch, "rows per COPY / INSERT batch")
	dryRun := fl.Bool("dry-run", false, "only check versions and emptiness and count rows; change nothing")
	checkEmpty := fl.Bool("force-empty-check", true, "refuse a target that holds data (false: replace its data)")
	if err := fl.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *from == "" || *to == "" || fl.NArg() > 0 {
		return fmt.Errorf("usage: omnigate migrate-db --from <url> --to <url> [--batch 1000] [--dry-run] [--force-empty-check=false]")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	_, err := transfer.Run(ctx, transfer.Options{From: *from, To: *to, Batch: *batch, DryRun: *dryRun, SkipEmptyCheck: !*checkEmpty,
		Out: os.Stdout, Log: slog.New(slog.NewTextHandler(os.Stderr, nil))})
	if err != nil {
		return fmt.Errorf("migrate-db: %w", err)
	}
	return nil
}

// healthcheck GETs /healthz on the local listener derived from addr (default
// ":8080"); a wildcard or empty host means loopback.
func healthcheck(addr string) error {
	if addr = strings.TrimSpace(addr); addr == "" {
		addr = ":8080"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("healthcheck: bad OMNIGATE_HTTP_ADDR %q: %w", addr, err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + net.JoinHostPort(host, port) + "/healthz")
	if err != nil {
		return fmt.Errorf("healthcheck: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck: /healthz returned %s", resp.Status)
	}
	return nil
}

func newLogger(cfg *config.Config) *slog.Logger {
	var level slog.Level
	if err := level.UnmarshalText([]byte(strings.ToUpper(cfg.LogLevel))); err != nil {
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	if cfg.LogFormat == "text" {
		return slog.New(slog.NewTextHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, opts))
}
