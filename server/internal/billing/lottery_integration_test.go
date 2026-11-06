package billing

import (
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/audit"
	"omnigate/internal/identity"
	"omnigate/internal/platform/db"
	"omnigate/internal/protocol"
	"omnigate/internal/subscription"
)

func TestLotteryHistoricalAndLiveSpend(t *testing.T) {
	f := newFixture(t)
	user := f.user(identity.RoleUser)
	if err := db.MigrateDownTo(f.ctx, f.pool, 24); err != nil {
		t.Fatal(err)
	}
	midnight := time.Date(2026, 11, 5, 16, 0, 0, 0, time.UTC)
	admin := f.user(identity.RoleSystemAdmin)
	sa := subscription.Actor{ID: admin.ID, Name: admin.Name}
	subs := subscription.NewService(f.pool, audit.NewRecorder(f.pool, slog.Default()), slog.Default())
	plan, err := subs.CreatePlan(f.ctx, sa, subscription.PlanInput{Name: "test", Duration: "30d", Rules: []subscription.Rule{{ID: "total", Meter: subscription.MeterCharge, Limit: "99", Window: subscription.Window{Kind: subscription.WindowLifetime}}}}, subscription.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	sub, _, err := subs.AdminGrant(f.ctx, sa, user.ID, plan.ID, 1, subscription.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	// Insert genuine pre-upgrade ledger rows: only model debits earn chances.
	f.credit(user.ID, "100")
	historicalDebit := func(at time.Time, amount, ref string) {
		t.Helper()
		f.svc.now = func() time.Time { return at }
		err := db.InTx(f.ctx, f.pool, func(tx db.Tx) error {
			w, err := f.svc.lockWallet(f.ctx, tx, user.ID, false)
			if err != nil {
				return err
			}
			_, err = f.svc.applyLedger(f.ctx, tx, w, KindCharge, -amt(amount), ref, uuid.NewString(), nil, nil)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	historicalDebit(midnight.Add(-time.Minute), "5", RefRequest)
	historicalDebit(midnight, "10", RefRequest)
	historicalDebit(midnight, "39", "purchase")
	_, err = f.pool.Exec(f.ctx, "INSERT INTO subscription_charges(request_id,subscription_id,model,quota_charge_nano,created_at) VALUES($1,$2,'m',5000000001,$3)", uuid.NewString(), sub.ID, midnight.Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(f.ctx, f.pool, slog.Default()); err != nil {
		t.Fatal(err)
	}
	f.svc.now = func() time.Time { return midnight.Add(time.Minute) }
	f.credit(user.ID, "100")
	check := func(available int, spend string) {
		t.Helper()
		v, err := f.svc.Lottery(f.ctx, user.ID)
		if err != nil || v.Available != available || v.TodaySpent != spend {
			t.Fatalf("status %+v, err %v", v, err)
		}
	}
	check(1, "10")
	f.setEnforce(true)
	requestID := uuid.NewString()
	for range 2 {
		if err := subs.Record(f.ctx, sub.ID, requestID, "m", protocol.Usage{}, amt("0.01"), midnight.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	check(2, "10.01")
	requestID = uuid.NewString()
	for range 2 {
		if err := f.svc.Settle(f.ctx, user.ID, requestID, amt("0.02")); err != nil {
			t.Fatal(err)
		}
	}
	check(2, "10.03")
	if err := subs.Record(f.ctx, sub.ID, uuid.NewString(), "m", protocol.Usage{}, amt("0.01"), midnight.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	c, err := lotteryConfig(f.ctx, f.pool)
	if err != nil {
		t.Fatal(err)
	}
	c.Threshold = "0"
	c, err = f.svc.SetLottery(f.ctx, admin, c, RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	check(3, "10.03")
	c.Threshold = "100"
	if _, err = f.svc.SetLottery(f.ctx, admin, c, RequestMeta{}); err != nil {
		t.Fatal(err)
	}
	check(3, "10.03")
}

func TestLotteryConcurrentBatchesAndRetry(t *testing.T) {
	f := newFixture(t)
	user := f.user(identity.RoleUser)
	admin := f.user(identity.RoleSystemAdmin)
	c, err := lotteryConfig(f.ctx, f.pool)
	if err != nil {
		t.Fatal(err)
	}
	for _, cents := range []struct {
		roll int
		want string
	}{{0, "0.5"}, {14, "0.88"}, {15, "1"}, {89, "2"}, {90, "2.5"}, {99, "3"}} {
		n, err := prizeAt(c.Prizes, cents.roll)
		if err != nil || n.String() != cents.want {
			t.Fatalf("roll %d = %v %v", cents.roll, n, err)
		}
	}
	c.Prizes = []LotteryPrize{{"3.01", 100}}
	if _, err = f.svc.SetLottery(f.ctx, admin, c, RequestMeta{}); err == nil {
		t.Fatal("accepted prize above cap")
	}
	c.Prizes = []LotteryPrize{{"1.25", 100}}
	if _, err = f.svc.SetLottery(f.ctx, admin, c, RequestMeta{}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 36; i++ {
		_, err := f.pool.Exec(f.ctx, "INSERT INTO lottery_days VALUES($1,$2,11000000000,true)", user.ID, f.svc.now().AddDate(0, 0, -i).Format(time.DateOnly))
		if err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	key := uuid.New()
	for range 4 {
		wg.Go(func() {
			r, err := f.svc.DrawLottery(f.ctx, user.ID, key, 20)
			if err != nil || len(r.Rewards) != 20 || r.Total != "25" {
				t.Errorf("draw %+v err %v", r, err)
			}
		})
	}
	wg.Wait()
	if _, err := f.svc.DrawLottery(f.ctx, user.ID, key, 10); err == nil {
		t.Fatal("accepted reused key for different count")
	}
	for _, n := range []int{10, 5} {
		if _, err := f.svc.DrawLottery(f.ctx, user.ID, uuid.New(), n); err != nil {
			t.Fatal(err)
		}
	}
	wins := make(chan bool, 2)
	for range 2 {
		wg.Go(func() { _, err := f.svc.DrawLottery(f.ctx, user.ID, uuid.New(), 1); wins <- err == nil })
	}
	wg.Wait()
	if <-wins == <-wins {
		t.Fatal("last chance must be consumed exactly once")
	}
	if v, err := f.svc.Lottery(f.ctx, user.ID); err != nil || v.Available != 0 || f.wallet(user.ID).Balance.String() != "45" {
		t.Fatalf("settlement %+v, %v", v, err)
	}
	f.checkLedger(user.ID)
}
