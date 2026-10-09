package limits

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/time/rate"

	"omnigate/internal/keys"
	"omnigate/internal/money"
	"omnigate/internal/platform/db"
	"omnigate/internal/usergroup"
)

// Denial codes (gateway error classes, §2.2).
const (
	CodeRateLimited  = "rate_limited"
	CodeRequestLimit = "user_request_limit"
	CodeSpendLimit   = "spend_limit_exceeded"
)

// Spend limit kinds reported to notifications.
const (
	KindDaily   = "daily"
	KindMonthly = "monthly"
	KindKey     = "key"
)

// Denial is a request rejected by a limit.
type Denial struct {
	Code    string
	Message string
	// RetryAfter is when the limit resets (0 = never, e.g. a 'total' key limit).
	RetryAfter time.Duration
}

// SpendEvent is a spend limit at ≥ 80 % (or reached) after a settlement
// (notifications limit.spend_near / limit.spend_reached).
type SpendEvent struct {
	UserID uuid.UUID
	Kind   string // daily | monthly | key
	// KeyID / KeyName identify the key of a key limit.
	KeyID       *uuid.UUID
	KeyName     string
	Window      Window
	WindowStart time.Time
	ResetsAt    *time.Time
	Spent       money.Amount
	Limit       money.Amount
	Reached     bool
}

// Service applies the limits.
type Service struct {
	pool   *db.DB
	groups *usergroup.Service
	log    *slog.Logger
	now    func() time.Time

	mu      sync.Mutex
	buckets map[uuid.UUID]*rate.Limiter // per-user RPM (this instance)
	sweep   time.Time

	// OnSpend receives spend limits at ≥ 80 % after a committed settlement.
	// It must not block.
	OnSpend func(ctx context.Context, e SpendEvent)
}

// NewService creates the limits service.
func NewService(pool *db.DB, groups *usergroup.Service, log *slog.Logger) *Service {
	return &Service{pool: pool, groups: groups, log: log, now: func() time.Time { return time.Now().UTC() },
		buckets: map[uuid.UUID]*rate.Limiter{}, sweep: time.Now()}
}

// Group returns the group for a key owner's group id (default when nil / unknown).
func (s *Service) Group(ctx context.Context, id *uuid.UUID) (*usergroup.Group, error) {
	return s.groups.Get(ctx, id)
}

func formatReset(t time.Time, loc *time.Location) string {
	return t.In(loc).Format("2006-01-02 15:04 MST")
}

// allowRPM takes a token from the user's bucket; when empty it returns the
// wait until the next token.
func (s *Service) allowRPM(userID uuid.UUID, rpm int, now time.Time) (bool, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Since(s.sweep) > 10*time.Minute {
		for k, b := range s.buckets {
			if b.TokensAt(now) >= float64(b.Burst()) {
				delete(s.buckets, k)
			}
		}
		s.sweep = time.Now()
	}
	b, ok := s.buckets[userID]
	if !ok || b.Burst() != rpm {
		b = rate.NewLimiter(rate.Limit(float64(rpm)/60), rpm)
		s.buckets[userID] = b
	}
	r := b.ReserveN(now, 1)
	if !r.OK() {
		return false, time.Minute
	}
	if d := r.DelayFrom(now); d > 0 {
		r.CancelAt(now)
		return false, d
	}
	return true, 0
}

// CheckRequest applies the group's rpm (per instance) and rpd before the
// first upstream attempt of a request (every tier: own and shared channels
// are subject to them too).
func (s *Service) CheckRequest(ctx context.Context, userID uuid.UUID, g *usergroup.Group, at time.Time) (*Denial, error) {
	if g.Limits.RPM != nil {
		if ok, wait := s.allowRPM(userID, *g.Limits.RPM, s.now()); !ok {
			return &Denial{Code: CodeRateLimited, RetryAfter: max(wait, time.Second),
				Message: fmt.Sprintf("user request rate limit exceeded (group %q: %d requests per minute)", g.Name, *g.Limits.RPM)}, nil
		}
	}
	if g.Limits.RPD != nil {
		loc := g.Location()
		reqs, _, err := read(ctx, s.pool, ScopeUser, userID, Day, Start(Day, at, loc))
		if err != nil {
			return nil, err
		}
		if reqs >= int64(*g.Limits.RPD) {
			end := *End(Day, at, loc)
			return &Denial{Code: CodeRequestLimit, RetryAfter: max(end.Sub(s.now()), time.Second),
				Message: fmt.Sprintf("daily request limit reached (group %q: %d requests per day); resets at %s",
					g.Name, *g.Limits.RPD, formatReset(end, loc))}, nil
		}
	}
	return nil, nil
}

// spendLimit is one applicable spend limit.
type spendLimit struct {
	kind   string
	scope  string
	id     uuid.UUID
	window Window
	limit  money.Amount
}

func applicable(userID uuid.UUID, key *keys.Key, g *usergroup.Group) []spendLimit {
	var out []spendLimit
	if g.Limits.DailySpend != nil {
		out = append(out, spendLimit{KindDaily, ScopeUser, userID, Day, *g.Limits.DailySpend})
	}
	if g.Limits.MonthlySpend != nil {
		out = append(out, spendLimit{KindMonthly, ScopeUser, userID, Month, *g.Limits.MonthlySpend})
	}
	if key != nil {
		if a, ok := key.Policy.SpendLimit.Cap(); ok && Window(key.Policy.SpendLimit.Window).Valid() {
			out = append(out, spendLimit{KindKey, ScopeKey, key.ID, Window(key.Policy.SpendLimit.Window), a})
		}
	}
	return out
}

func describe(l spendLimit, key *keys.Key) string {
	switch l.kind {
	case KindDaily:
		return "daily spend limit of your user group"
	case KindMonthly:
		return "monthly spend limit of your user group"
	}
	name := ""
	if key != nil {
		name = " " + fmt.Sprintf("%q", key.Name)
	}
	return string(l.window) + " spend limit of API key" + name
}

// CheckSpend applies the group's daily / monthly spend limits and the key's
// spend limit before the first platform attempt of a charged request. It
// returns the smallest remaining amount (nil = unlimited), used to cap the
// wallet reservation.
func (s *Service) CheckSpend(ctx context.Context, userID uuid.UUID, key *keys.Key, g *usergroup.Group, at time.Time) (*money.Amount, *Denial, error) {
	ls := applicable(userID, key, g)
	if len(ls) == 0 {
		return nil, nil, nil
	}
	loc := g.Location()
	var remaining *money.Amount
	for _, l := range ls {
		_, spent, err := read(ctx, s.pool, l.scope, l.id, l.window, Start(l.window, at, loc))
		if err != nil {
			return nil, nil, err
		}
		if spent >= l.limit {
			d := &Denial{Code: CodeSpendLimit, Message: fmt.Sprintf("%s reached (%s / %s)", describe(l, key), spent, l.limit)}
			if end := End(l.window, at, loc); end != nil {
				d.RetryAfter = max(end.Sub(s.now()), time.Second)
				d.Message += "; resets at " + formatReset(*end, loc)
			} else {
				d.Message += "; it never resets (raise the limit to continue)"
			}
			return nil, d, nil
		}
		left := l.limit - spent
		if remaining == nil || left < *remaining {
			remaining = &left
		}
	}
	return remaining, nil, nil
}

// Usage describes a finished request for the counters.
type Usage struct {
	UserID uuid.UUID
	Key    *keys.Key
	Group  *usergroup.Group
	At     time.Time // request start
	// Counted: the request passed the request limits (it counts for rpd).
	Counted bool
	// Charge is the wallet charge of a platform request (0 otherwise).
	Charge money.Amount
}

// Increments returns the counter increments of u: the user's day and month
// requests (when counted) and charge, and the key's charge in every window
// (so that a key limit can change window without losing history).
func Increments(u Usage) []Increment {
	if !u.Counted && u.Charge <= 0 {
		return nil
	}
	loc := u.Group.Location()
	var reqs int64
	if u.Counted {
		reqs = 1
	}
	charge := max(u.Charge, 0)
	out := []Increment{
		{Scope: ScopeUser, ScopeID: u.UserID, Window: Day, Start: Start(Day, u.At, loc), Requests: reqs, Charge: charge},
		{Scope: ScopeUser, ScopeID: u.UserID, Window: Month, Start: Start(Month, u.At, loc), Requests: reqs, Charge: charge},
	}
	if charge > 0 && u.Key != nil {
		for _, w := range []Window{Day, Week, Month, Total} {
			out = append(out, Increment{Scope: ScopeKey, ScopeID: u.Key.ID, Window: w, Start: Start(w, u.At, loc), Charge: charge})
		}
	}
	return out
}

// Recorder applies the increments of one settlement and, after commit,
// reports spend limits at ≥ 80 %.
type Recorder struct {
	s       *Service
	u       Usage
	incs    []Increment
	results []Counter
}

// Recorder returns the recorder of u (nil when there is nothing to count).
func (s *Service) Recorder(u Usage) *Recorder {
	incs := Increments(u)
	if len(incs) == 0 {
		return nil
	}
	return &Recorder{s: s, u: u, incs: incs}
}

// Increments returns the counter increments (journaled with a failed
// settlement and applied with Apply on replay).
func (r *Recorder) Increments() []Increment { return append([]Increment(nil), r.incs...) }

// Apply writes the increments in q (the settlement transaction).
func (r *Recorder) Apply(ctx context.Context, q db.Querier) error {
	out, err := Apply(ctx, q, r.incs, r.s.now())
	if err != nil {
		return err
	}
	r.results = out
	return nil
}

// ApplyNow writes the increments in a transaction of their own (requests
// that need no wallet settlement).
func (r *Recorder) ApplyNow(ctx context.Context) error {
	return db.InTx(ctx, r.s.pool, func(tx db.Tx) error { return r.Apply(ctx, tx) })
}

// Committed reports spend limits at ≥ 80 % after the transaction committed.
func (r *Recorder) Committed(ctx context.Context) {
	if r.s.OnSpend == nil || r.u.Charge <= 0 {
		return
	}
	loc := r.u.Group.Location()
	for _, l := range applicable(r.u.UserID, r.u.Key, r.u.Group) {
		if l.limit <= 0 {
			continue
		}
		start := Start(l.window, r.u.At, loc)
		for _, c := range r.results {
			if c.Scope != l.scope || c.ScopeID != l.id || c.Window != l.window || !c.Start.Equal(start) {
				continue
			}
			if !nearLimit(c.Charge, l.limit) {
				continue
			}
			e := SpendEvent{UserID: r.u.UserID, Kind: l.kind, Window: l.window, WindowStart: start, ResetsAt: End(l.window, r.u.At, loc),
				Spent: c.Charge, Limit: l.limit, Reached: c.Charge >= l.limit}
			if l.kind == KindKey {
				id := r.u.Key.ID
				e.KeyID, e.KeyName = &id, r.u.Key.Name
			}
			r.s.OnSpend(ctx, e)
		}
	}
}

// nearLimit reports spent ≥ 80 % of limit (spent × 5 ≥ limit × 4).
func nearLimit(spent, limit money.Amount) bool {
	a, err1 := spent.MulDiv(5, 1)
	b, err2 := limit.MulDiv(4, 1)
	if err1 != nil || err2 != nil {
		return spent >= limit-limit/5
	}
	return a >= b
}
