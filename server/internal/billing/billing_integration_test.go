package billing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"omnigate/internal/apperr"
	"omnigate/internal/audit"
	"omnigate/internal/auth"
	"omnigate/internal/authz"
	"omnigate/internal/identity"
	"omnigate/internal/money"
	"omnigate/internal/platform/db"
	"omnigate/internal/platform/db/dbtest"
	"omnigate/internal/subscription"
)

// testDB returns an isolated, migrated database for this test (PostgreSQL or
// SQLite, see dbtest). Requires OMNIGATE_TEST_DATABASE_URL (skips otherwise).
func testDB(t *testing.T) *db.DB {
	t.Helper()
	return dbtest.Open(t)
}

type fixture struct {
	t    *testing.T
	ctx  context.Context
	pool *db.DB
	svc  *Service
}

func newFixture(t *testing.T) *fixture {
	pool := testDB(t)
	log := slog.New(slog.DiscardHandler)
	svc := NewService(pool, audit.NewRecorder(pool, log), log)
	return &fixture{t: t, ctx: context.Background(), pool: pool, svc: svc}
}

func (f *fixture) user(role identity.Role) Actor {
	f.t.Helper()
	id := uuid.Must(uuid.NewV7())
	name := "u-" + id.String()[:8]
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO users (id, display_name, role) VALUES ($1, $2, $3)`, id, name, role); err != nil {
		f.t.Fatal(err)
	}
	return Actor{ID: id, Name: name}
}

func (f *fixture) setEnforce(v bool) {
	f.t.Helper()
	st, err := f.svc.Settings(f.ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	admin := f.user(identity.RoleSystemAdmin)
	if _, err := f.svc.UpdateSettings(f.ctx, admin, v, st.Version, RequestMeta{}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) batch(in BatchInput) (*Batch, []string) {
	f.t.Helper()
	admin := f.user(identity.RoleSystemAdmin)
	b, codes, err := f.svc.CreateBatch(f.ctx, admin, in, RequestMeta{})
	if err != nil {
		f.t.Fatal(err)
	}
	return b, codes
}

func (f *fixture) wallet(id uuid.UUID) Wallet {
	f.t.Helper()
	w, err := f.svc.GetWallet(f.ctx, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return w
}

// credit gives the user balance through an admin adjustment.
func (f *fixture) credit(id uuid.UUID, amount string) {
	f.t.Helper()
	admin := f.user(identity.RoleSystemAdmin)
	w := f.wallet(id)
	if _, _, err := f.svc.AdjustWallet(f.ctx, admin, id, AdjustInput{Amount: money.MustParse(amount), Note: "test", Version: w.Version}, RequestMeta{}); err != nil {
		f.t.Fatal(err)
	}
}

func errCode(err error) string {
	if err == nil {
		return ""
	}
	return apperr.As(err).Code
}

func amt(s string) money.Amount { return money.MustParse(s) }

// checkLedger verifies that balance_after is the running sum of amounts and
// matches the cached wallet balance.
func (f *fixture) checkLedger(userID uuid.UUID) {
	f.t.Helper()
	entries, _, err := f.svc.ListLedger(f.ctx, userID, 0, 1000)
	if err != nil {
		f.t.Fatal(err)
	}
	var sum money.Amount
	for i := len(entries) - 1; i >= 0; i-- { // oldest first
		sum += entries[i].Amount
		if entries[i].BalanceAfter != sum {
			f.t.Fatalf("entry %d (%s %s): balance_after %s, running sum %s", i, entries[i].Kind, entries[i].Amount,
				entries[i].BalanceAfter, sum)
		}
	}
	if w := f.wallet(userID); w.Balance != sum {
		f.t.Fatalf("wallet balance %s != ledger sum %s", w.Balance, sum)
	}
}

func TestConcurrentRedeemSingleUseCode(t *testing.T) {
	f := newFixture(t)
	_, codes := f.batch(BatchInput{Amount: amt("10"), Count: 1})
	users := make([]Actor, 20)
	for i := range users {
		users[i] = f.user(identity.RoleUser)
	}
	var wg sync.WaitGroup
	errs := make([]error, len(users))
	for i, u := range users {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = f.svc.Redeem(f.ctx, u, codes[0], RequestMeta{})
		}()
	}
	wg.Wait()
	success := 0
	var total money.Amount
	for i, err := range errs {
		switch errCode(err) {
		case "":
			success++
		case "redeem_used_up":
		default:
			t.Fatalf("user %d: unexpected error %v", i, err)
		}
		total += f.wallet(users[i].ID).Balance
	}
	if success != 1 || total != amt("10") {
		t.Fatalf("success=%d total credited=%s; want 1 and 10", success, total)
	}
	var redemptions, used int
	if err := f.pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM redemptions), (SELECT sum(used_count) FROM redeem_codes)`).
		Scan(&redemptions, &used); err != nil {
		t.Fatal(err)
	}
	if redemptions != 1 || used != 1 {
		t.Fatalf("redemptions=%d used=%d", redemptions, used)
	}
	var audits int
	_ = f.pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_logs WHERE action = 'billing.redeem'`).Scan(&audits)
	if audits != 1 {
		t.Fatalf("audit rows = %d", audits)
	}
}

func TestRedeemRules(t *testing.T) {
	f := newFixture(t)
	u := f.user(identity.RoleUser)

	// Multi-use codes with per-user limit 2.
	_, codes := f.batch(BatchInput{Amount: amt("1.5"), Count: 3, MaxRedemptionsPerCode: 5, PerUserLimit: 2})
	res, err := f.svc.Redeem(f.ctx, u, codes[0], RequestMeta{})
	if err != nil || res.Amount != amt("1.5") || res.Wallet.Balance != amt("1.5") {
		t.Fatalf("first redeem: %+v %v", res, err)
	}
	// Same code again, lower case and without hyphens: counts toward the limit.
	if _, err := f.svc.Redeem(f.ctx, u, " og"+lower(stripHyphens(codes[0][2:]))+" ", RequestMeta{}); err != nil {
		t.Fatalf("second redeem (normalized input): %v", err)
	}
	if _, err := f.svc.Redeem(f.ctx, u, codes[1], RequestMeta{}); errCode(err) != "redeem_user_limit" {
		t.Fatalf("third redeem: want redeem_user_limit, got %v", err)
	}
	if w := f.wallet(u.ID); w.Balance != amt("3") {
		t.Fatalf("balance %s", w.Balance)
	}

	// Unknown and malformed codes.
	u2 := f.user(identity.RoleUser)
	unknown, _, _ := GenerateCode()
	for _, c := range []string{unknown, "not-a-code", ""} {
		_, err := f.svc.Redeem(f.ctx, u2, c, RequestMeta{})
		if e := apperr.As(err); e.Code != "redeem_invalid" || e.Kind != apperr.KindValidation {
			t.Fatalf("code %q: want redeem_invalid, got %v", c, err)
		}
	}

	// Not started.
	future := time.Now().Add(time.Hour)
	_, nsCodes := f.batch(BatchInput{Amount: amt("1"), Count: 1, ValidFrom: &future})
	if _, err := f.svc.Redeem(f.ctx, u2, nsCodes[0], RequestMeta{}); errCode(err) != "redeem_not_started" {
		t.Fatalf("want redeem_not_started, got %v", err)
	}

	// Expired (moved into the past directly; the API refuses past expiry).
	exp, expCodes := f.batch(BatchInput{Amount: amt("1"), Count: 1})
	if _, err := f.pool.Exec(f.ctx, `UPDATE redeem_batches SET expires_at = $2 WHERE id = $1`, exp.ID, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Redeem(f.ctx, u2, expCodes[0], RequestMeta{}); errCode(err) != "redeem_expired" {
		t.Fatalf("want redeem_expired, got %v", err)
	}

	// Disabled, then re-enabled.
	u3 := f.user(identity.RoleUser)
	admin := f.user(identity.RoleSystemAdmin)
	dis, disCodes := f.batch(BatchInput{Amount: amt("2"), Count: 1})
	if _, err := f.svc.UpdateBatchStatus(f.ctx, admin, dis.ID, BatchDisabled, nil, RequestMeta{}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Redeem(f.ctx, u3, disCodes[0], RequestMeta{}); errCode(err) != "redeem_batch_disabled" {
		t.Fatalf("want redeem_batch_disabled, got %v", err)
	}
	stale := 1
	if _, err := f.svc.UpdateBatchStatus(f.ctx, admin, dis.ID, BatchActive, &stale, RequestMeta{}); errCode(err) != "version_conflict" {
		t.Fatalf("want version_conflict, got %v", err)
	}
	if _, err := f.svc.UpdateBatchStatus(f.ctx, admin, dis.ID, BatchActive, nil, RequestMeta{}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Redeem(f.ctx, u3, disCodes[0], RequestMeta{}); err != nil {
		t.Fatalf("redeem after re-enable: %v", err)
	}
	f.checkLedger(u.ID)
	f.checkLedger(u3.ID)

	// Batch list shows redeemed counts.
	list, total, err := f.svc.ListBatches(f.ctx, 0, 50)
	if err != nil || total != 4 {
		t.Fatalf("list: %d %v", total, err)
	}
	for _, b := range list {
		if b.ID == dis.ID && b.Redeemed != 1 {
			t.Fatalf("disabled batch redeemed=%d", b.Redeemed)
		}
	}
	// Plaintext codes are never stored or audited.
	var leaked int
	_ = f.pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_logs WHERE metadata::text LIKE '%' || $1 || '%'`, disCodes[0][3:]).Scan(&leaked)
	if leaked != 0 {
		t.Fatal("plaintext code found in audit log")
	}
}

func TestRedeemRateLimit(t *testing.T) {
	f := newFixture(t)
	u := f.user(identity.RoleUser)
	for i := range 5 {
		if _, err := f.svc.Redeem(f.ctx, u, "bogus", RequestMeta{}); errCode(err) != "redeem_invalid" {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if _, err := f.svc.Redeem(f.ctx, u, "bogus", RequestMeta{}); errCode(err) != "rate_limited" {
		t.Fatalf("6th attempt: want rate_limited, got %v", err)
	}
	// Other users are unaffected.
	if _, err := f.svc.Redeem(f.ctx, f.user(identity.RoleUser), "bogus", RequestMeta{}); errCode(err) != "redeem_invalid" {
		t.Fatalf("other user: %v", err)
	}
}

func TestReserveSettleEnforced(t *testing.T) {
	f := newFixture(t)
	f.setEnforce(true)
	if on, err := f.svc.Enforced(f.ctx); err != nil || !on {
		t.Fatalf("Enforced = %v, %v", on, err)
	}
	u := f.user(identity.RoleUser)

	// No wallet / zero balance → insufficient.
	if err := f.svc.Reserve(f.ctx, u.ID, "r0", amt("0.01")); errCode(err) != "insufficient_balance" {
		t.Fatalf("want insufficient_balance, got %v", err)
	}
	var ie *apperr.Error
	if err := f.svc.Reserve(f.ctx, u.ID, "r0", 0); !errors.As(err, &ie) || ie.Kind != apperr.KindForbidden {
		t.Fatalf("zero available must be rejected even for zero estimate: %v", err)
	}

	f.credit(u.ID, "10")
	if err := f.svc.Reserve(f.ctx, u.ID, "r1", amt("6")); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Reserve(f.ctx, u.ID, "r1", amt("6")); err != nil { // idempotent
		t.Fatal(err)
	}
	w := f.wallet(u.ID)
	if w.Reserved != amt("6") || w.Available() != amt("4") {
		t.Fatalf("after reserve: %+v", w)
	}
	// An estimate above the available balance is admitted, capped at the available balance.
	if err := f.svc.Reserve(f.ctx, u.ID, "r2", amt("5")); err != nil {
		t.Fatal(err)
	}
	if w := f.wallet(u.ID); w.Reserved != amt("10") || w.Available() != 0 {
		t.Fatalf("capped reservation: %+v", w)
	}
	// Nothing available any more → rejected.
	if err := f.svc.Reserve(f.ctx, u.ID, "r2b", amt("0.000000001")); errCode(err) != "insufficient_balance" {
		t.Fatalf("want insufficient_balance with nothing available, got %v", err)
	}

	// Settle twice charges once.
	if err := f.svc.Settle(f.ctx, u.ID, "r1", amt("2.5")); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Settle(f.ctx, u.ID, "r1", amt("2.5")); err != nil {
		t.Fatal(err)
	}
	w = f.wallet(u.ID)
	if w.Balance != amt("7.5") || w.Reserved != amt("4") {
		t.Fatalf("after settle r1: %+v", w)
	}

	// Charge larger than the reservation may drive the balance negative.
	if err := f.svc.Settle(f.ctx, u.ID, "r2", amt("9")); err != nil {
		t.Fatal(err)
	}
	w = f.wallet(u.ID)
	if w.Balance != amt("-1.5") || w.Reserved != 0 {
		t.Fatalf("after settle r2: %+v", w)
	}
	if err := f.svc.Reserve(f.ctx, u.ID, "r3", amt("0.000000001")); errCode(err) != "insufficient_balance" {
		t.Fatalf("negative balance must block: %v", err)
	}

	// Settle without a reservation (e.g. swept) still charges; zero charge only releases.
	f.credit(u.ID, "5")
	if err := f.svc.Settle(f.ctx, u.ID, "r4", amt("0.25")); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Reserve(f.ctx, u.ID, "r5", amt("1")); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Settle(f.ctx, u.ID, "r5", 0); err != nil {
		t.Fatal(err)
	}
	w = f.wallet(u.ID)
	if w.Balance != amt("3.25") || w.Reserved != 0 {
		t.Fatalf("after r4/r5: %+v", w)
	}
	var charges int
	_ = f.pool.QueryRow(f.ctx, `SELECT count(*) FROM ledger_entries WHERE kind = 'charge'`).Scan(&charges)
	if charges != 3 {
		t.Fatalf("charge entries = %d, want 3", charges)
	}
	f.checkLedger(u.ID)
}

func TestConcurrentSettleChargesOnce(t *testing.T) {
	f := newFixture(t)
	f.setEnforce(true)
	u := f.user(identity.RoleUser)
	f.credit(u.ID, "10")
	if err := f.svc.Reserve(f.ctx, u.ID, "req", amt("3")); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := f.svc.Settle(f.ctx, u.ID, "req", amt("1")); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if w := f.wallet(u.ID); w.Balance != amt("9") || w.Reserved != 0 {
		t.Fatalf("wallet %+v", w)
	}
	f.checkLedger(u.ID)
}

func TestEnforceOffIsNoop(t *testing.T) {
	f := newFixture(t)
	if on, err := f.svc.Enforced(f.ctx); err != nil || on {
		t.Fatalf("default Enforced = %v, %v", on, err)
	}
	u := f.user(identity.RoleUser)
	if err := f.svc.Reserve(f.ctx, u.ID, "a", amt("100")); err != nil {
		t.Fatalf("reserve with enforce off: %v", err)
	}
	if err := f.svc.Settle(f.ctx, u.ID, "a", amt("5")); err != nil {
		t.Fatal(err)
	}
	var wallets, entries int
	_ = f.pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM wallets), (SELECT count(*) FROM ledger_entries)`).Scan(&wallets, &entries)
	if wallets != 0 || entries != 0 {
		t.Fatalf("enforce off touched balances: wallets=%d entries=%d", wallets, entries)
	}

	// A reservation made while enforced is released (but nothing charged) after
	// enforcement is switched off.
	f.credit(u.ID, "10")
	f.setEnforce(true)
	if err := f.svc.Reserve(f.ctx, u.ID, "b", amt("4")); err != nil {
		t.Fatal(err)
	}
	f.setEnforce(false)
	if err := f.svc.Settle(f.ctx, u.ID, "b", amt("4")); err != nil {
		t.Fatal(err)
	}
	if w := f.wallet(u.ID); w.Balance != amt("10") || w.Reserved != 0 {
		t.Fatalf("wallet %+v", w)
	}
}

func TestSweepExpiredReservations(t *testing.T) {
	f := newFixture(t)
	f.setEnforce(true)
	u1, u2 := f.user(identity.RoleUser), f.user(identity.RoleUser)
	f.credit(u1.ID, "10")
	f.credit(u2.ID, "10")
	for _, r := range []struct {
		u  Actor
		id string
	}{{u1, "x1"}, {u1, "x2"}, {u1, "keep"}, {u2, "y1"}} {
		if err := f.svc.Reserve(f.ctx, r.u.ID, r.id, amt("1")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.pool.Exec(f.ctx, `UPDATE reservations SET expires_at = $1 WHERE request_id <> 'keep'`, time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	n, err := f.svc.SweepExpiredReservations(f.ctx)
	if err != nil || n != 3 {
		t.Fatalf("swept %d, %v; want 3", n, err)
	}
	if w := f.wallet(u1.ID); w.Reserved != amt("1") || w.Balance != amt("10") {
		t.Fatalf("u1 %+v", w)
	}
	if w := f.wallet(u2.ID); w.Reserved != 0 {
		t.Fatalf("u2 %+v", w)
	}
	if n, _ := f.svc.SweepExpiredReservations(f.ctx); n != 0 {
		t.Fatalf("second sweep released %d", n)
	}
	// Settling a swept request still charges exactly once.
	if err := f.svc.Settle(f.ctx, u2.ID, "y1", amt("0.5")); err != nil {
		t.Fatal(err)
	}
	if w := f.wallet(u2.ID); w.Balance != amt("9.5") || w.Reserved != 0 {
		t.Fatalf("u2 after settle %+v", w)
	}
}

func TestAdminAdjust(t *testing.T) {
	f := newFixture(t)
	admin := f.user(identity.RoleSystemAdmin)
	u := f.user(identity.RoleUser)
	w := f.wallet(u.ID)
	if w.Balance != 0 || w.Version != 1 {
		t.Fatalf("missing wallet view %+v", w)
	}
	w2, e, err := f.svc.AdjustWallet(f.ctx, admin, u.ID, AdjustInput{Amount: amt("12.345"), Note: "promo", Version: 1}, RequestMeta{})
	if err != nil || w2.Balance != amt("12.345") || w2.Version != 2 || e.Kind != KindAdjust || e.RefType != RefAdmin {
		t.Fatalf("adjust: %+v %+v %v", w2, e, err)
	}
	if _, _, err := f.svc.AdjustWallet(f.ctx, admin, u.ID, AdjustInput{Amount: amt("-1"), Note: "x", Version: 1}, RequestMeta{}); errCode(err) != "version_conflict" {
		t.Fatalf("stale version: want version_conflict, got %v", err)
	}
	if _, _, err := f.svc.AdjustWallet(f.ctx, admin, u.ID, AdjustInput{Amount: amt("-20"), Note: "clawback", Version: 2}, RequestMeta{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.svc.AdjustWallet(f.ctx, admin, u.ID, AdjustInput{Amount: 0, Note: "x", Version: 3}, RequestMeta{}); errCode(err) != "validation_failed" {
		t.Fatalf("zero amount: %v", err)
	}
	if _, _, err := f.svc.AdjustWallet(f.ctx, admin, uuid.Must(uuid.NewV7()), AdjustInput{Amount: amt("1"), Note: "x", Version: 1}, RequestMeta{}); errCode(err) != "not_found" {
		t.Fatalf("unknown user: %v", err)
	}
	f.checkLedger(u.ID)
	var audits int
	_ = f.pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_logs WHERE action = 'billing.wallet_adjust'`).Scan(&audits)
	if audits != 2 {
		t.Fatalf("audit rows %d", audits)
	}
}

// ---- HTTP ----

type httpHarness struct {
	t   *testing.T
	srv *httptest.Server
}

func newHTTP(t *testing.T, f *fixture) *httpHarness {
	h := NewHandler(f.svc)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			var p *authz.Principal
			if id, err := uuid.Parse(req.Header.Get("X-Test-User")); err == nil {
				p = &authz.Principal{UserID: id, Name: "t", Role: identity.Role(req.Header.Get("X-Test-Role"))}
			}
			next.ServeHTTP(w, req.WithContext(auth.WithPrincipal(req.Context(), p)))
		})
	})
	r.Route("/api", func(r chi.Router) {
		h.Routes(r)
		r.Route("/admin", h.AdminRoutes)
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return &httpHarness{t: t, srv: srv}
}

func (h *httpHarness) do(as Actor, role identity.Role, method, path string, body any) (int, map[string]any) {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, h.srv.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-User", as.ID.String())
	req.Header.Set("X-Test-Role", string(role))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func errOf(m map[string]any) string {
	e, _ := m["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

func TestHTTP(t *testing.T) {
	f := newFixture(t)
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO system_settings (key, value) VALUES ('billing.currency', '{"code":"CNY","symbol":"¥","decimals":2}')`); err != nil {
		t.Fatal(err)
	}
	h := newHTTP(t, f)
	admin := f.user(identity.RoleSystemAdmin)
	user := f.user(identity.RoleUser)
	const A, U = identity.RoleSystemAdmin, identity.RoleUser

	// Empty wallet.
	st, body := h.do(user, U, "GET", "/api/billing/wallet", nil)
	if st != 200 || body["balance"] != "0" || body["available"] != "0" || body["currency"] != "CNY" {
		t.Fatalf("wallet: %d %v", st, body)
	}

	// Permissions.
	if st, body := h.do(user, U, "GET", "/api/admin/billing/settings", nil); st != 403 || errOf(body) != "forbidden" {
		t.Fatalf("user on admin: %d %v", st, body)
	}
	if st, _ := h.do(user, identity.RoleAuditor, "GET", "/api/billing/wallet", nil); st != 403 {
		t.Fatalf("auditor wallet: %d", st)
	}

	// Settings.
	st, body = h.do(admin, A, "GET", "/api/admin/billing/settings", nil)
	if st != 200 || body["enforce"] != false || body["version"] != float64(1) {
		t.Fatalf("settings: %d %v", st, body)
	}
	if st, body := h.do(admin, A, "PUT", "/api/admin/billing/settings", map[string]any{"enforce": true, "version": 7}); st != 409 || errOf(body) != "version_conflict" {
		t.Fatalf("settings stale: %d %v", st, body)
	}
	st, body = h.do(admin, A, "PUT", "/api/admin/billing/settings", map[string]any{"enforce": true, "version": 1})
	if st != 200 || body["enforce"] != true || body["version"] != float64(2) {
		t.Fatalf("settings put: %d %v", st, body)
	}

	// Create batch.
	if st, body := h.do(admin, A, "POST", "/api/admin/billing/redeem-batches", map[string]any{"amount": "0", "count": 1001}); st != 422 {
		t.Fatalf("invalid batch: %d %v", st, body)
	}
	st, body = h.do(admin, A, "POST", "/api/admin/billing/redeem-batches", map[string]any{"amount": "5.25", "count": 3, "note": "launch"})
	if st != 201 {
		t.Fatalf("create batch: %d %v", st, body)
	}
	batch := body["batch"].(map[string]any)
	codes := body["codes"].([]any)
	if len(codes) != 3 || batch["amount"] != "5.25" || batch["count"] != float64(3) || batch["status"] != "active" ||
		batch["kind"] != "wallet_credit" || batch["redeemed"] != float64(0) {
		t.Fatalf("batch: %v codes %v", batch, codes)
	}

	// Redeem.
	st, body = h.do(user, U, "POST", "/api/billing/redeem", map[string]any{"code": codes[0]})
	if st != 200 || body["kind"] != "wallet_credit" || body["amount"] != "5.25" || body["wallet"].(map[string]any)["balance"] != "5.25" {
		t.Fatalf("redeem: %d %v", st, body)
	}
	if st, body := h.do(user, U, "POST", "/api/billing/redeem", map[string]any{"code": codes[0]}); st != 409 || errOf(body) != "redeem_used_up" {
		t.Fatalf("reuse: %d %v", st, body)
	}
	if st, body := h.do(user, U, "POST", "/api/billing/redeem", map[string]any{"code": "OG-XXXXX"}); st != 422 || errOf(body) != "redeem_invalid" {
		t.Fatalf("invalid: %d %v", st, body)
	}

	// List batches.
	st, body = h.do(admin, A, "GET", "/api/admin/billing/redeem-batches?page=1&pageSize=10", nil)
	items := body["items"].([]any)
	if st != 200 || body["total"] != float64(1) || items[0].(map[string]any)["redeemed"] != float64(1) {
		t.Fatalf("list batches: %d %v", st, body)
	}

	// Disable batch.
	id := batch["id"].(string)
	st, body = h.do(admin, A, "PATCH", "/api/admin/billing/redeem-batches/"+id, map[string]any{"status": "disabled"})
	if st != 200 || body["status"] != "disabled" {
		t.Fatalf("patch: %d %v", st, body)
	}
	if st, body := h.do(user, U, "POST", "/api/billing/redeem", map[string]any{"code": codes[1]}); st != 409 || errOf(body) != "redeem_batch_disabled" {
		t.Fatalf("disabled: %d %v", st, body)
	}
	if st, _ := h.do(admin, A, "PATCH", "/api/admin/billing/redeem-batches/"+uuid.NewString(), map[string]any{"status": "active"}); st != 404 {
		t.Fatalf("patch unknown: %d", st)
	}

	// Admin wallet + adjust.
	st, body = h.do(admin, A, "GET", "/api/admin/billing/wallets/"+user.ID.String(), nil)
	if st != 200 || body["balance"] != "5.25" {
		t.Fatalf("admin wallet: %d %v", st, body)
	}
	ver := body["version"]
	if st, body := h.do(admin, A, "POST", "/api/admin/billing/wallets/"+user.ID.String()+"/adjust",
		map[string]any{"amount": "-0.25", "note": "", "version": ver}); st != 422 {
		t.Fatalf("adjust without note: %d %v", st, body)
	}
	st, body = h.do(admin, A, "POST", "/api/admin/billing/wallets/"+user.ID.String()+"/adjust",
		map[string]any{"amount": "-0.25", "note": "fix", "version": ver})
	if st != 200 || body["wallet"].(map[string]any)["balance"] != "5" || body["entry"].(map[string]any)["amount"] != "-0.25" {
		t.Fatalf("adjust: %d %v", st, body)
	}
	if st, body := h.do(admin, A, "POST", "/api/admin/billing/wallets/"+user.ID.String()+"/adjust",
		map[string]any{"amount": "1", "note": "again", "version": ver}); st != 409 || errOf(body) != "version_conflict" {
		t.Fatalf("adjust stale: %d %v", st, body)
	}
	if st, _ := h.do(admin, A, "GET", "/api/admin/billing/wallets/"+uuid.NewString(), nil); st != 404 {
		t.Fatalf("unknown user wallet: %d", st)
	}

	// Ledger, newest first.
	st, body = h.do(user, U, "GET", "/api/billing/ledger", nil)
	items = body["items"].([]any)
	if st != 200 || body["total"] != float64(2) || len(items) != 2 {
		t.Fatalf("ledger: %d %v", st, body)
	}
	first, second := items[0].(map[string]any), items[1].(map[string]any)
	if first["kind"] != "adjust" || first["balanceAfter"] != "5" || first["note"] != "fix" ||
		second["kind"] != "grant" || second["refType"] != "redeem" || second["amount"] != "5.25" {
		t.Fatalf("ledger items: %v", items)
	}

	// Audit trail for admin mutations.
	var n int
	_ = f.pool.QueryRow(f.ctx, `SELECT count(DISTINCT action) FROM audit_logs WHERE action IN
		('billing.batch_create','billing.batch_update','billing.wallet_adjust','billing.settings_update','billing.redeem')`).Scan(&n)
	if n != 5 {
		t.Fatalf("distinct billing audit actions = %d", n)
	}
}

func lower(s string) string { return string(bytes.ToLower([]byte(s))) }

func stripHyphens(s string) string { return string(bytes.ReplaceAll([]byte(s), []byte("-"), nil)) }

func TestRedeemPlan(t *testing.T) {
	f := newFixture(t)
	log := slog.New(slog.DiscardHandler)
	subs := subscription.NewService(f.pool, audit.NewRecorder(f.pool, log), log)
	admin := f.user(identity.RoleSystemAdmin)
	sa := subscription.Actor{ID: admin.ID, Name: admin.Name}
	plan, err := subs.CreatePlan(f.ctx, sa, subscription.PlanInput{Name: "Pro", Duration: "30d",
		Rules: []subscription.Rule{{ID: "n", Meter: subscription.MeterRequests, Limit: "10",
			Window: subscription.Window{Kind: subscription.WindowLifetime}}}}, subscription.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}

	// Batch validation.
	for name, in := range map[string]BatchInput{
		"planId,periods": {Kind: BatchKindPlan, Count: 1},
		"periods":        {Kind: BatchKindPlan, PlanID: &plan.ID, Periods: 121, Count: 1},
		"amount":         {Kind: BatchKindPlan, PlanID: &plan.ID, Periods: 1, Amount: amt("1"), Count: 1},
		"kind":           {Kind: "invite", Count: 1},
	} {
		_, _, err := f.svc.CreateBatch(f.ctx, admin, in, RequestMeta{})
		e := apperr.As(err)
		if e.Code != "validation_failed" {
			t.Fatalf("%s: %v", name, err)
		}
		for _, k := range bytes.Split([]byte(name), []byte(",")) {
			if e.Details[string(k)] == nil {
				t.Fatalf("%s: details %v", name, e.Details)
			}
		}
	}
	unknown := uuid.New()
	if _, _, err := f.svc.CreateBatch(f.ctx, admin, BatchInput{Kind: BatchKindPlan, PlanID: &unknown, Periods: 1, Count: 1},
		RequestMeta{}); apperr.As(err).Details["planId"] == nil {
		t.Fatalf("unknown plan: %v", err)
	}

	b, codes := f.batch(BatchInput{Kind: BatchKindPlan, PlanID: &plan.ID, Periods: 2, Count: 3, PerUserLimit: 2})
	if b.Kind != BatchKindPlan || *b.Periods != 2 || *b.PlanID != plan.ID || *b.PlanName != "Pro" || b.Amount != 0 {
		t.Fatalf("batch %+v", b)
	}
	u := f.user(identity.RoleUser)
	res, err := f.svc.Redeem(f.ctx, u, codes[0], RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	sv := res.Subscription
	if res.Kind != BatchKindPlan || res.Renewed || sv == nil || sv.Source != subscription.SourceRedeem ||
		sv.EndsAt.Sub(sv.StartsAt) != 60*24*time.Hour || sv.User.ID != u.ID || sv.Plan.Name != "Pro" {
		t.Fatalf("first redeem %+v %+v", res, sv)
	}
	res2, err := f.svc.Redeem(f.ctx, u, codes[1], RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if !res2.Renewed || res2.Subscription.ID != sv.ID || !res2.Subscription.EndsAt.Equal(sv.EndsAt.Add(60*24*time.Hour)) {
		t.Fatalf("renew %+v", res2.Subscription)
	}
	if _, err := f.svc.Redeem(f.ctx, u, codes[2], RequestMeta{}); errCode(err) != "redeem_user_limit" {
		t.Fatalf("per-user limit: %v", err)
	}
	var wallets, linked int
	_ = f.pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM wallets),
		(SELECT count(*) FROM redemptions WHERE subscription_id = $1 AND ledger_entry_id IS NULL)`, sv.ID).Scan(&wallets, &linked)
	if wallets != 0 || linked != 2 {
		t.Fatalf("wallets=%d linked redemptions=%d", wallets, linked)
	}
	var audits int
	_ = f.pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_logs WHERE action = 'billing.redeem' AND metadata->>'kind' = 'plan'`).Scan(&audits)
	if audits != 2 {
		t.Fatalf("redeem audits %d", audits)
	}

	// HTTP: plan batch creation and redemption response shape.
	h := newHTTP(t, f)
	st, body := h.do(admin, identity.RoleSystemAdmin, "POST", "/api/admin/billing/redeem-batches",
		map[string]any{"kind": "plan", "planId": plan.ID, "periods": 1, "count": 1})
	batch, _ := body["batch"].(map[string]any)
	if st != 201 || batch["kind"] != "plan" || batch["amount"] != nil || batch["periods"] != float64(1) || batch["planName"] != "Pro" {
		t.Fatalf("create plan batch: %d %v", st, body)
	}
	if st, body := h.do(admin, identity.RoleSystemAdmin, "POST", "/api/admin/billing/redeem-batches",
		map[string]any{"kind": "plan", "planId": plan.ID, "count": 1}); st != 422 {
		t.Fatalf("missing periods: %d %v", st, body)
	}
	u2 := f.user(identity.RoleUser)
	st, body = h.do(u2, identity.RoleUser, "POST", "/api/billing/redeem", map[string]any{"code": body["codes"].([]any)[0]})
	sub, _ := body["subscription"].(map[string]any)
	if st != 200 || body["kind"] != "plan" || sub["status"] != "active" || body["wallet"] != nil ||
		sub["rules"].([]any)[0].(map[string]any)["remaining"] != "10" {
		t.Fatalf("redeem plan: %d %v", st, body)
	}

	// Archived plans: no new batches, and existing codes stop working.
	_, moreCodes := f.batch(BatchInput{Kind: BatchKindPlan, PlanID: &plan.ID, Periods: 1, Count: 1})
	archived := subscription.PlanArchived
	if _, err := subs.UpdatePlan(f.ctx, sa, plan.ID, subscription.PlanPatch{Status: &archived, Version: 1}, subscription.RequestMeta{}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Redeem(f.ctx, f.user(identity.RoleUser), moreCodes[0], RequestMeta{}); errCode(err) != "plan_archived" {
		t.Fatalf("redeem archived: %v", err)
	}
	if _, _, err := f.svc.CreateBatch(f.ctx, admin, BatchInput{Kind: BatchKindPlan, PlanID: &plan.ID, Periods: 1, Count: 1},
		RequestMeta{}); errCode(err) != "plan_archived" {
		t.Fatalf("batch for archived plan: %v", err)
	}
	// The failed redemption left the code unused.
	var used int
	_ = f.pool.QueryRow(f.ctx, `SELECT used_count FROM redeem_codes WHERE code_hash = $1`, HashCode(mustNorm(moreCodes[0]))).Scan(&used)
	if used != 0 {
		t.Fatalf("archived redemption consumed the code")
	}
}

func mustNorm(code string) string {
	n, ok := NormalizeCode(code)
	if !ok {
		panic(code)
	}
	return n
}

// TestConcurrentReservations runs many concurrent reserve/settle cycles on
// one wallet. Reservations, settlements and the ledger must stay consistent
// (no lost updates) on both PostgreSQL and SQLite.
func TestConcurrentReservations(t *testing.T) {
	f := newFixture(t)
	f.setEnforce(true)
	u := f.user(identity.RoleUser)
	f.credit(u.ID, "10")
	const workers, each = 8, 10
	var wg sync.WaitGroup
	var mu sync.Mutex
	settled := 0
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range each {
				id := fmt.Sprintf("req-%d-%d", w, i)
				err := f.svc.Reserve(f.ctx, u.ID, id, amt("0.5"))
				if errCode(err) == "insufficient_balance" {
					continue
				}
				if err != nil {
					t.Error(err)
					return
				}
				if err := f.svc.Settle(f.ctx, u.ID, id, amt("0.1")); err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				settled++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	w := f.wallet(u.ID)
	if w.Reserved != 0 || w.Balance != amt("10")-money.Amount(settled)*amt("0.1") {
		t.Fatalf("wallet %+v after %d settlements", w, settled)
	}
	var reservations int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM reservations`).Scan(&reservations); err != nil || reservations != 0 {
		t.Fatalf("reservations left: %d %v", reservations, err)
	}
	f.checkLedger(u.ID)
}
