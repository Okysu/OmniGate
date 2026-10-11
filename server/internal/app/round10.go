package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"omnigate/internal/config"
	"omnigate/internal/gateway"
	"omnigate/internal/journal"
	"omnigate/internal/platform/db"
	"omnigate/internal/requestlog"
)

// Durable settlement (docs/adr/0010-durable-settlement.md): settlements and
// request log batches that cannot be written to the database are kept in a
// local journal under OMNIGATE_DATA_DIR and replayed in order once the
// database is reachable (at startup and by a background worker).

// journalInterval is how often the replayer looks at pending journal items.
const journalInterval = 5 * time.Second

// openJournal opens the settlement journal and lets the request log writer
// spill into it.
func openJournal(cfg *config.Config, log *slog.Logger, pool *db.DB, logs *requestlog.Writer, opts Options) (*journal.Journal, error) {
	j, err := journal.Open(cfg.DataDir, log)
	if err != nil {
		return nil, fmt.Errorf("OMNIGATE_DATA_DIR %q: %w", cfg.DataDir, err)
	}
	j.Ping = pool.Ping
	logs.Journal = j
	logs.BeforeInsert = opts.BeforeLogInsert
	logs.FlushInterval = opts.LogFlushInterval
	return j, nil
}

// handleJournal registers the replay handlers.
func handleJournal(j *journal.Journal, gw *gateway.Gateway, logs *requestlog.Writer) {
	j.Handle(gateway.SettlementKind, gw.ReplaySettlement)
	j.Handle(requestlog.JournalKind, logs.Replay)
}

// gatewayBilling applies Options.WrapBilling (tests).
func gatewayBilling(b gateway.Billing, opts Options) gateway.Billing {
	if opts.WrapBilling != nil {
		return opts.WrapBilling(b)
	}
	return b
}

// journalState is the journal part of /readyz.
type journalState struct {
	Pending int `json:"pending"`
}

func (a *App) journalStatus() journalState { return journalState{Pending: a.journal.Pending()} }

// closeJournal syncs and closes the journal (pending items stay on disk).
func (a *App) closeJournal() {
	if err := a.journal.Close(); err != nil {
		a.log.Error("close settlement journal", "err", err)
	}
}

// ReplayJournal replays pending journal items now and returns how many were
// applied (tests, diagnostics).
func (a *App) ReplayJournal(ctx context.Context) (int, error) { return a.journal.Replay(ctx) }

// JournalPending returns the number of journal items waiting for replay.
func (a *App) JournalPending() int { return a.journal.Pending() }
