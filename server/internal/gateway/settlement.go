package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/journal"
	"omnigate/internal/limits"
	"omnigate/internal/money"
	"omnigate/internal/platform/db"
	"omnigate/internal/subscription"
)

// SettlementKind is the settlement journal kind of a request settlement.
const SettlementKind = "settlement"

// Settlement is the durable settlement of one finished request (ADR-0010):
// the wallet charge or hold release and the usage counters (one
// transaction), then the plan quota record. Every step is idempotent per
// request id — charges by their ledger entry, counters by settled_requests,
// quota usage by subscription_charges — so a settlement can be retried and
// replayed from the journal any number of times. A charge does not depend on
// the reservation: if the sweeper released an expired hold meanwhile, the
// actual amount is still debited.
type Settlement struct {
	RequestID string    `json:"requestId"`
	UserID    uuid.UUID `json:"userId"`
	// Wallet settles the wallet: Charge is debited (billing.enforce) and any
	// hold of the request released.
	Wallet bool         `json:"wallet,omitempty"`
	Charge money.Amount `json:"charge,omitempty"`
	// Increments are the usage counter increments of the request limits.
	Increments []limits.Increment `json:"increments,omitempty"`
	// Quota is the usage of a plan-covered request.
	Quota *subscription.RecordInput `json:"quota,omitempty"`
}

// settleAttempts and settleRetryDelay bound the in-process retries of a
// settlement before it is journaled.
const (
	settleAttempts   = 3
	settleRetryDelay = 250 * time.Millisecond
	settleTimeout    = 15 * time.Second
)

// runSettlement applies s with retries (one attempt while the journal already
// holds items: the database is known to be down) and journals what is left
// when it keeps failing. rec (nil on replay) reports spend limits after commit.
func (g *Gateway) runSettlement(s *Settlement, rec *limits.Recorder) {
	attempts := settleAttempts
	if g.opts.Journal != nil && g.opts.Journal.Pending() > 0 {
		attempts = 1
	}
	delay := settleRetryDelay
	var err error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			time.Sleep(delay)
			delay *= 4
		}
		ctx, cancel := context.WithTimeout(context.Background(), settleTimeout)
		err = g.applySettlement(ctx, s, rec)
		cancel()
		if err == nil {
			return
		}
		g.log.Warn("settlement failed", "request_id", s.RequestID, "attempt", i+1, "err", err)
	}
	journal.Failed(SettlementKind)
	if j := g.opts.Journal; j != nil {
		jerr := j.Append(SettlementKind, s)
		if jerr == nil {
			g.log.Error("settlement failed; saved to the settlement journal for replay", "request_id", s.RequestID, "err", err)
			return
		}
		err = errors.Join(err, jerr)
	}
	raw, _ := json.Marshal(s)
	g.log.Error("settlement lost: database and journal unavailable", "request_id", s.RequestID, "err", err, "settlement", string(raw))
}

// applySettlement runs the steps of s that are not done yet; a finished step
// is cleared from s so that a retry or the journaled copy skips it.
func (g *Gateway) applySettlement(ctx context.Context, s *Settlement, rec *limits.Recorder) error {
	if s.Wallet || len(s.Increments) > 0 {
		var record func(context.Context, db.Querier) error
		switch {
		case rec != nil:
			record = rec.Apply
		case len(s.Increments) > 0:
			incs := s.Increments
			record = func(ctx context.Context, q db.Querier) error {
				_, err := limits.Apply(ctx, q, incs, time.Now().UTC())
				return err
			}
		}
		if err := g.billing.SettleUsage(ctx, s.UserID, s.RequestID, s.Charge, record); err != nil {
			return fmt.Errorf("wallet settlement: %w", err)
		}
		s.Wallet, s.Charge, s.Increments = false, 0, nil
		if rec != nil {
			rec.Committed(ctx)
		}
	}
	if s.Quota != nil {
		if g.quota != nil {
			if err := g.quota.RecordUsage(ctx, *s.Quota); err != nil {
				return fmt.Errorf("quota record: %w", err)
			}
		}
		s.Quota = nil
	}
	return nil
}

// ReplaySettlement applies a journaled settlement (settlement journal
// handler). Spend limit notifications are not sent for replayed settlements.
func (g *Gateway) ReplaySettlement(ctx context.Context, data json.RawMessage) error {
	var s Settlement
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("decode journaled settlement: %w", err)
	}
	return g.applySettlement(ctx, &s, nil)
}
