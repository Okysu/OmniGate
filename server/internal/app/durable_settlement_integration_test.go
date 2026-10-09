package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"omnigate/internal/app"
	"omnigate/internal/billing"
	"omnigate/internal/gateway"
	"omnigate/internal/journal"
	"omnigate/internal/money"
	"omnigate/internal/platform/db"
)

// P0-1 (security audit): in prepaid mode a request the wallet pays for is
// admitted only while the available balance is > 0, whatever its estimate.
// This is the audit's proof of concept turned into a regression test.
func TestPrepaidZeroBalanceAdmission(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir()) // spooled multipart bodies
	e := setupGateway(t)
	base := e.h.srv.URL
	up := newFakeUpstream(t)
	aup := newAudioUpstream(t)
	close(aup.release)
	e.platformChannel(map[string]any{"name": "chat", "type": "openai", "scope": "global", "baseUrl": up.srv.URL + "/v1",
		"models": models("c1", "c2", "c3")})
	e.platformChannel(map[string]any{"name": "aud", "type": "openai", "scope": "global", "baseUrl": aup.srv.URL + "/v1",
		"models": imgModels("whisper-z", "whisper-1")})
	e.mustDo(e.admin, http.MethodPost, "/api/admin/prices", map[string]any{"kind": "sell", "model": "c1", "inputPerM": "1000", "outputPerM": "2000"}, 201)
	e.mustDo(e.admin, http.MethodPost, "/api/admin/prices", map[string]any{"kind": "sell", "model": "c2", "outputPerM": "2000"}, 201)
	e.mustDo(e.admin, http.MethodPost, "/api/admin/prices", map[string]any{"kind": "sell", "model": "whisper-z", "perMinute": "6"}, 201)
	e.enforceBilling()
	carolID := e.userID(e.carol)
	_, key := e.key(e.carol, map[string]any{"name": "c"})

	chatCall := func(body string) (int, map[string]any, string) {
		t.Helper()
		return readBody(gwPost(t, context.Background(), base, "/v1/chat/completions", key, body))
	}
	whisper := func() (int, map[string]any, string) {
		t.Helper()
		ct, body := mpBody(field("model", "whisper-z"), field("response_format", "json"), audioFile(randomBytes(100)))
		return readBody(gwPostCT(t, base, "/v1/audio/transcriptions", key, ct, bytes.NewReader(body)))
	}
	expect402 := func(name string, code int, out map[string]any, raw string) {
		t.Helper()
		if code != 402 || errCode(out) != "insufficient_balance" {
			t.Fatalf("%s at zero balance = %d %s", name, code, raw)
		}
	}
	hits0, ahits0 := up.hits.Load(), aup.hits.Load()
	c, out, raw := chatCall(`{"model":"c1","messages":[{"role":"user","content":"hi"}]}`)
	expect402("control", c, out, raw)
	c, out, raw = chatCall(`{"model":"c1","max_output_tokens":9223372036854775807,"messages":[{"role":"user","content":"hi"}]}`)
	expect402("max_output_tokens overflow", c, out, raw)
	c, out, raw = chatCall(`{"model":"c2","messages":[{"role":"user","content":"hi"}]}`)
	expect402("output-only price without max_tokens", c, out, raw)
	c, out, raw = whisper()
	expect402("per-minute audio price", c, out, raw)
	if up.hits.Load() != hits0 || aup.hits.Load() != ahits0 {
		t.Fatal("a rejected request reached the upstream")
	}

	// A small positive balance admits them, with the hold capped at it, and
	// settlement charges the actual usage.
	e.credit(carolID, "1")
	if c, _, raw := chatCall(`{"model":"c2","messages":[{"role":"user","content":"hi"}]}`); c != 200 {
		t.Fatalf("c2 with balance = %d %s", c, raw)
	}
	e.settle()
	wallet := func() (string, string) {
		t.Helper()
		w := e.mustDo(e.carol, http.MethodGet, "/api/billing/wallet", nil, 200)
		return w["balance"].(string), w["reserved"].(string)
	}
	if bal, res := wallet(); bal != "0.996" || res != "0" { // 2 output tokens × 2000/M
		t.Fatalf("after c2: balance %s reserved %s", bal, res)
	}
	up.setMode("block")
	resp := gwPost(t, context.Background(), base, "/v1/chat/completions", key,
		`{"model":"c1","stream":true,"max_output_tokens":9223372036854775807,"messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != 200 {
		t.Fatalf("c1 stream with balance = %d", resp.StatusCode)
	}
	buf := make([]byte, 16)
	if _, err := resp.Body.Read(buf); err != nil {
		t.Fatal(err)
	}
	// max_output_tokens is clamped to 1M (2000 at this price): the hold is
	// capped at the available balance.
	if _, res := wallet(); res != "0.996" {
		t.Fatalf("hold while streaming = %s", res)
	}
	close(up.release)
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	e.settle()
	if bal, res := wallet(); bal != "0.982" || res != "0" { // 10 × 1000/M + 2 × 2000/M
		t.Fatalf("after c1: balance %s reserved %s", bal, res)
	}
	// Per-minute audio: estimate 0, admitted with a positive balance; the
	// actual 61 s are charged even though nothing was held.
	if c, _, raw := whisper(); c != 200 {
		t.Fatalf("whisper with balance = %d %s", c, raw)
	}
	e.settle()
	if bal, _ := wallet(); bal != "-5.118" {
		t.Fatalf("after whisper: balance %s", bal)
	}
	c, out, raw = chatCall(`{"model":"c2","messages":[{"role":"user","content":"hi"}]}`)
	expect402("negative balance", c, out, raw)

	// A request that cannot be priced is never admitted (whatever the balance).
	e.credit(carolID, "100")
	e.mustDo(e.admin, http.MethodPost, "/api/admin/prices", map[string]any{"kind": "sell", "model": "c3", "perRequest": "100000000",
		"outputPerM": "9200000000"}, 201)
	c, out, raw = chatCall(`{"model":"c3","max_tokens":1000000,"messages":[{"role":"user","content":"hi"}]}`)
	if c != 400 || errCode(out) != "invalid_request" {
		t.Fatalf("unpriceable request = %d %s", c, raw)
	}

	// Spend limits: a reached limit rejects an estimate-0 request too.
	_, capped := e.key(e.carol, map[string]any{"name": "capped", "policy": map[string]any{"spendLimit": map[string]any{"amount": "0.004", "window": "total"}}})
	call := func(k string) (int, map[string]any, string) {
		return readBody(gwPost(t, context.Background(), base, "/v1/chat/completions", k, `{"model":"c2","messages":[{"role":"user","content":"hi"}]}`))
	}
	up.setMode("ok")
	if c, _, raw := call(capped); c != 200 {
		t.Fatalf("first capped call = %d %s", c, raw)
	}
	e.settle()
	if c, out, raw := call(capped); c != 429 && c != 402 || errCode(out) != "spend_limit_exceeded" {
		t.Fatalf("capped key after its limit = %d %s", c, raw)
	}
}

// flakyBilling fails settlements while fail is set (a database outage).
type flakyBilling struct {
	gateway.Billing
	fail  atomic.Bool
	calls atomic.Int64
}

func (f *flakyBilling) SettleUsage(ctx context.Context, userID uuid.UUID, requestID string, charge money.Amount,
	record func(context.Context, db.Querier) error) error {
	f.calls.Add(1)
	if f.fail.Load() {
		return errors.New("simulated database outage")
	}
	return f.Billing.SettleUsage(ctx, userID, requestID, charge, record)
}

// gaugeValue reads a gauge of the default registry.
func gaugeValue(t *testing.T, name string) float64 {
	t.Helper()
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() == name && len(mf.GetMetric()) > 0 {
			return mf.GetMetric()[0].GetGauge().GetValue()
		}
	}
	return 0
}

// durableEnv is a gateway whose settlements and request log inserts can be
// made to fail.
type durableEnv struct {
	*gwEnv
	bill    *flakyBilling
	logFail atomic.Int64 // >0: fail that many inserts; <0: fail all
	key     string       // carol's key
	planKey string       // alice's key (plan-covered)
	carolID string
	aliceID string
}

func setupDurable(t *testing.T) *durableEnv {
	d := &durableEnv{bill: &flakyBilling{}}
	opts := app.Options{
		WrapBilling: func(b gateway.Billing) gateway.Billing { d.bill.Billing = b; return d.bill },
		BeforeLogInsert: func(context.Context) error {
			if n := d.logFail.Load(); n < 0 || n > 0 && d.logFail.CompareAndSwap(n, n-1) {
				return errors.New("simulated request log insert failure")
			}
			return nil
		},
	}
	d.gwEnv = setupGatewayWith(t, opts)
	e := d.gwEnv
	up := newFakeUpstream(t)
	e.platformChannel(map[string]any{"name": "chat", "type": "openai", "scope": "global", "baseUrl": up.srv.URL + "/v1",
		"models": models("c1", "p1")})
	e.mustDo(e.admin, http.MethodPost, "/api/admin/prices", map[string]any{"kind": "sell", "model": "c1", "inputPerM": "1000", "outputPerM": "2000"}, 201)
	e.mustDo(e.admin, http.MethodPost, "/api/admin/prices", map[string]any{"kind": "sell", "model": "p1", "inputPerM": "1000"}, 201)
	e.enforceBilling()
	d.carolID, d.aliceID = e.userID(e.carol), e.userID(e.admin)
	e.credit(d.carolID, "10")
	_, d.key = e.key(e.carol, map[string]any{"name": "c"})
	// alice's p1 requests are covered by a plan (quota usage instead of the wallet).
	plan := e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/plans", map[string]any{"name": "p", "description": "", "duration": "30d",
		"models": []string{"p1"}, "stackable": false, "rules": []map[string]any{{"id": "r", "meter": "requests",
			"window": map[string]any{"kind": "lifetime"}, "limit": "100"}}}, 201)
	e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/subscriptions", map[string]any{"userId": d.aliceID, "planId": plan["id"], "periods": 1}, 201)
	_, d.planKey = e.key(e.admin, map[string]any{"name": "a"})
	return d
}

func (d *durableEnv) call(key, model string) {
	d.t.Helper()
	if c, _, raw := readBody(gwPost(d.t, context.Background(), d.h.srv.URL, "/v1/chat/completions", key,
		`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`)); c != 200 {
		d.t.Fatalf("%s = %d %s", model, c, raw)
	}
}

// state is what settlement wrote for carol and alice.
type settled struct {
	balance, reserved int64
	charges           int // carol's charge ledger entries
	logs              int // request log rows
	userRequests      int64
	planCharges       int // subscription_charges rows
}

func (d *durableEnv) state() settled {
	d.t.Helper()
	ctx := context.Background()
	var s settled
	q := func(sql string, dst any, args ...any) {
		d.t.Helper()
		if err := d.app.DB().QueryRow(ctx, sql, args...).Scan(dst); err != nil {
			d.t.Fatalf("%s: %v", sql, err)
		}
	}
	q(`SELECT balance_nano FROM wallets WHERE user_id = $1`, &s.balance, d.carolID)
	q(`SELECT reserved_nano FROM wallets WHERE user_id = $1`, &s.reserved, d.carolID)
	q(`SELECT count(*) FROM ledger_entries l JOIN wallets w ON w.id = l.wallet_id WHERE w.user_id = $1 AND l.kind = 'charge'`, &s.charges, d.carolID)
	q(`SELECT count(*) FROM request_logs`, &s.logs)
	q(`SELECT COALESCE(sum(requests), 0) FROM usage_counters WHERE scope = 'user' AND window_kind = 'day'`, &s.userRequests)
	q(`SELECT count(*) FROM subscription_charges`, &s.planCharges)
	return s
}

func (d *durableEnv) readyz() map[string]any {
	d.t.Helper()
	resp, err := http.Get(d.h.srv.URL + "/readyz")
	if err != nil {
		d.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != 200 {
		d.t.Fatalf("readyz = %d %v", resp.StatusCode, out)
	}
	return out
}

// Settlements and request logs that fail during a database outage are
// journaled and replayed exactly once after recovery, even when the hold
// expired meanwhile and when a replay runs again.
func TestSettlementJournalReplay(t *testing.T) {
	d := setupDurable(t)
	ctx := context.Background()

	// A request log insert that fails once is retried in-process: nothing
	// is journaled.
	d.logFail.Store(1)
	d.call(d.key, "c1")
	if err := d.app.FlushLogs(ctx); err != nil {
		t.Fatal(err)
	}
	if n := d.app.JournalPending(); n != 0 {
		t.Fatalf("journal after a transient failure = %d", n)
	}
	base := d.state()
	if base.logs != 1 || base.charges != 1 || base.balance != 10e9-14e6 {
		t.Fatalf("baseline = %+v", base)
	}

	// Outage: settlements and request log inserts keep failing.
	settleFail0 := metricValue(t, "omnigate_settlement_failures_total", map[string]string{"kind": gateway.SettlementKind})
	logFail0 := metricValue(t, "omnigate_settlement_failures_total", map[string]string{"kind": "request_logs"})
	d.bill.fail.Store(true)
	d.logFail.Store(-1)
	d.call(d.key, "c1")
	d.call(d.planKey, "p1")
	if err := d.app.FlushLogs(ctx); err != nil {
		t.Fatalf("flush with a journal must not fail: %v", err)
	}
	if n := d.app.JournalPending(); n != 3 { // two settlements, one log batch
		t.Fatalf("journal pending = %d", n)
	}
	if v := metricValue(t, "omnigate_settlement_failures_total", map[string]string{"kind": gateway.SettlementKind}); v != settleFail0+2 {
		t.Fatalf("settlement failures metric = %v (before %v)", v, settleFail0)
	}
	if v := metricValue(t, "omnigate_settlement_failures_total", map[string]string{"kind": "request_logs"}); v != logFail0+1 {
		t.Fatalf("request log failures metric = %v (before %v)", v, logFail0)
	}
	if g := gaugeValue(t, "omnigate_journal_pending"); g != 3 {
		t.Fatalf("journal pending gauge = %v", g)
	}
	if r := d.readyz(); r["status"] != "degraded" || r["journal"].(map[string]any)["pending"] != float64(3) {
		t.Fatalf("readyz during outage = %v", r)
	}
	mid := d.state()
	if mid.charges != 1 || mid.logs != 1 || mid.reserved == 0 || mid.planCharges != 0 {
		t.Fatalf("during outage = %+v", mid)
	}
	raw, err := os.ReadFile(filepath.Join(d.cfg.DataDir, journal.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(filepath.Join(d.cfg.DataDir, journal.FileName)); st.Mode().Perm() != 0o600 {
		t.Fatalf("journal mode = %v", st.Mode())
	}

	// The hold expires during the outage and the sweeper releases it: the
	// journaled settlement must still charge the request.
	if _, err := d.app.DB().Exec(ctx, `UPDATE reservations SET expires_at = $1`, time.Now().UTC().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	sweeper := billing.NewService(d.app.DB(), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if n, err := sweeper.SweepExpiredReservations(ctx); err != nil || n != 1 {
		t.Fatalf("sweep = %d %v", n, err)
	}
	if s := d.state(); s.reserved != 0 {
		t.Fatalf("reserved after sweep = %d", s.reserved)
	}

	// Still down: a replay applies nothing and keeps the order.
	if n, err := d.app.ReplayJournal(ctx); n != 0 || err == nil {
		t.Fatalf("replay during outage = %d %v", n, err)
	}

	// Recovery.
	d.bill.fail.Store(false)
	d.logFail.Store(0)
	replayed0 := metricValue(t, "omnigate_journal_replayed_total", map[string]string{"kind": gateway.SettlementKind})
	// (The background replayer may get there first.)
	if _, err := d.app.ReplayJournal(ctx); err != nil || d.app.JournalPending() != 0 {
		t.Fatalf("replay = %v, pending %d", err, d.app.JournalPending())
	}
	if v := metricValue(t, "omnigate_journal_replayed_total", map[string]string{"kind": gateway.SettlementKind}); v != replayed0+2 {
		t.Fatalf("replayed metric = %v (before %v)", v, replayed0)
	}
	want := settled{balance: 10e9 - 2*14e6, reserved: 0, charges: 2, logs: 3, userRequests: 3, planCharges: 1}
	if got := d.state(); got != want {
		t.Fatalf("after replay = %+v, want %+v", got, want)
	}
	if r := d.readyz(); r["status"] != "ready" {
		t.Fatalf("readyz after replay = %v", r)
	}
	if used := ruleUsed(t, d.gwEnv); used["r"] != "1" {
		t.Fatalf("plan usage after replay = %v", used)
	}
	if n, err := d.app.ReplayJournal(ctx); n != 0 || err != nil {
		t.Fatalf("second replay = %d %v", n, err)
	}

	// A crash after the commits but before the acknowledgements replays
	// everything again on the next start: still exactly once.
	if err := os.WriteFile(filepath.Join(d.cfg.DataDir, journal.FileName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := app.New(ctx, d.cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), d.pool, app.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if n := b.JournalPending(); n != 3 {
		t.Fatalf("pending on restart = %d", n)
	}
	if n, err := b.ReplayJournal(ctx); n != 3 || err != nil {
		t.Fatalf("replay on restart = %d %v", n, err)
	}
	b.Stop()
	if got := d.state(); got != want {
		t.Fatalf("after a second replay = %+v, want %+v", got, want)
	}
	if used := ruleUsed(t, d.gwEnv); used["r"] != "1" {
		t.Fatalf("plan usage after a second replay = %v", used)
	}
}

// A real outage: the database connection pool is gone while a request is
// in flight. Its settlement and request log land in the journal, and a new
// process on the same data directory replays them at startup.
func TestSettlementJournalRestart(t *testing.T) {
	e := setupGateway(t)
	ctx := context.Background()
	up := newFakeUpstream(t)
	e.platformChannel(map[string]any{"name": "chat", "type": "openai", "scope": "global", "baseUrl": up.srv.URL + "/v1", "models": models("c1")})
	e.mustDo(e.admin, http.MethodPost, "/api/admin/prices", map[string]any{"kind": "sell", "model": "c1", "inputPerM": "1000", "outputPerM": "2000"}, 201)
	e.enforceBilling()
	carolID := e.userID(e.carol)
	e.credit(carolID, "10")
	_, key := e.key(e.carol, map[string]any{"name": "c"})

	// Instance A has its own pool and data directory.
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := *e.cfg
	cfg.DataDir = filepath.Join(t.TempDir(), "data")
	poolA, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.New(ctx, &cfg, log, poolA, app.Options{})
	if err != nil {
		t.Fatal(err)
	}
	srvA := httptest.NewServer(a.Handler())
	defer srvA.Close()
	time.Sleep(100 * time.Millisecond)

	up.setMode("block")
	resp := gwPost(t, ctx, srvA.URL, "/v1/chat/completions", key, `{"model":"c1","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != 200 {
		t.Fatalf("stream = %d", resp.StatusCode)
	}
	buf := make([]byte, 16)
	if _, err := resp.Body.Read(buf); err != nil {
		t.Fatal(err)
	}
	poolA.Close() // the database goes away mid-request
	close(up.release)
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if err := a.FlushLogs(ctx); err != nil {
		t.Fatal(err)
	}
	if n := a.JournalPending(); n != 2 {
		t.Fatalf("journal pending = %d", n)
	}
	a.Stop()

	var bal, reserved int64
	var logs int
	read := func() {
		t.Helper()
		if err := e.pool.QueryRow(ctx, `SELECT balance_nano, reserved_nano FROM wallets WHERE user_id = $1`, carolID).Scan(&bal, &reserved); err != nil {
			t.Fatal(err)
		}
		if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM request_logs`).Scan(&logs); err != nil {
			t.Fatal(err)
		}
	}
	read()
	if bal != 10e9 || reserved == 0 || logs != 0 {
		t.Fatalf("before restart: balance %d reserved %d logs %d", bal, reserved, logs)
	}

	// Restart: a new process (pool, app) on the same data directory replays
	// the journal at startup.
	poolB, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer poolB.Close()
	b, err := app.New(ctx, &cfg, log, poolB, app.Options{})
	if err != nil {
		t.Fatal(err)
	}
	bctx, cancel := context.WithCancel(ctx)
	b.Start(bctx)
	defer func() { cancel(); b.Stop() }()
	for i := 0; b.JournalPending() > 0; i++ {
		if i > 100 {
			t.Fatal("journal not replayed at startup")
		}
		time.Sleep(50 * time.Millisecond)
	}
	read()
	if bal != 10e9-14e6 || reserved != 0 || logs != 1 {
		t.Fatalf("after restart: balance %d reserved %d logs %d", bal, reserved, logs)
	}
	if st, err := os.Stat(filepath.Join(cfg.DataDir, journal.FileName)); err != nil || st.Size() != 0 {
		t.Fatalf("journal after replay: %v %v", st, err)
	}
}
