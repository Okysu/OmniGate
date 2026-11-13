package billing

import (
	"github.com/google/uuid"
	"omnigate/internal/identity"
	"omnigate/internal/subscription"
	"sync"
	"testing"
	"time"
)

func TestCampaignLotteryChancesAtomicConcurrentAndSpendable(t *testing.T) {
	f := newFixture(t)
	u := f.user(identity.RoleUser)
	c := newCampaign(t, f, Campaign{RewardType: "lottery", Reward: &CampaignReward{Quantity: 20}, Stock: 1})
	key := uuid.New()
	var wg sync.WaitGroup
	errors := make(chan error, 8)
	for range 8 {
		wg.Go(func() { _, err := f.svc.ClaimCampaign(f.ctx, u, c.ID, key, 0, RequestMeta{}); errors <- err })
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	status, err := f.svc.Lottery(f.ctx, u.ID)
	if err != nil || status.Available != 20 || status.Earned != 20 {
		t.Fatalf("status=%+v %v", status, err)
	}
	if _, err = f.svc.ClaimCampaign(f.ctx, u, c.ID, uuid.New(), 0, RequestMeta{}); err == nil {
		t.Fatal("stock/limit bypassed")
	}
	drawKey := uuid.New()
	result, err := f.svc.DrawLottery(f.ctx, u.ID, drawKey, 20)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := f.svc.DrawLottery(f.ctx, u.ID, drawKey, 20)
	if err != nil || result.LedgerID != retry.LedgerID {
		t.Fatal("draw retry", err)
	}
	status, err = f.svc.Lottery(f.ctx, u.ID)
	if err != nil || status.Available != 0 {
		t.Fatalf("after draw=%+v %v", status, err)
	}
	c.Reward = &CampaignReward{Quantity: 30}
	if _, err = f.svc.SaveCampaign(f.ctx, f.user(identity.RoleSystemAdmin), c.ID, *c, RequestMeta{}); err == nil {
		t.Fatal("changed claimed reward")
	}
}

func TestCampaignResetCardsAndEligibilityRecheck(t *testing.T) {
	f := newFixture(t)
	u := f.user(identity.RoleUser)
	other := f.user(identity.RoleUser)
	now := time.Now().UTC().Truncate(time.Second)
	f.svc.now = func() time.Time { return now }
	p := campaignPlan(t, f, 10, true)
	c := newCampaign(t, f, Campaign{RewardType: "reset_card", Reward: &CampaignReward{Quantity: 2, Kind: subscription.CardKindWeekly, ValidDays: 7, PlanIDs: []uuid.UUID{p.ID}}, Stock: 2, Conditions: CampaignConditions{UserIDs: []uuid.UUID{u.ID}}})
	if _, err := f.svc.ClaimCampaign(f.ctx, other, c.ID, uuid.New(), 0, RequestMeta{}); err == nil {
		t.Fatal("ineligible claim accepted")
	}
	key := uuid.New()
	out, err := f.svc.ClaimCampaign(f.ctx, u, c.ID, key, 0, RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	retry, err := f.svc.ClaimCampaign(f.ctx, u, c.ID, key, 0, RequestMeta{})
	if err != nil || out.ResetCardBatchID == nil || *out.ResetCardBatchID != *retry.ResetCardBatchID {
		t.Fatal("retry", err)
	}
	var count int
	var expiry time.Time
	if err = f.pool.QueryRow(f.ctx, "SELECT count(*),min(expires_at) FROM reset_cards WHERE user_id=$1", u.ID).Scan(&count, &expiry); err != nil {
		t.Fatal(err)
	}
	if count != 2 || !expiry.Equal(now.Add(7*24*time.Hour)) {
		t.Fatalf("cards=%d expiry=%v", count, expiry)
	}
	// Eligibility is resolved again at claim, not snapshotted at activity creation.
	c2 := newCampaign(t, f, Campaign{RewardType: "lottery", Reward: &CampaignReward{Quantity: 5}, Stock: 1, Conditions: CampaignConditions{RequireActiveSubscription: true, QuotaKind: "weekly", ExcludePlanIDs: []uuid.UUID{p.ID}}})
	if _, err = f.svc.ClaimCampaign(f.ctx, u, c2.ID, uuid.New(), 0, RequestMeta{}); err == nil {
		t.Fatal("no active weekly subscription")
	}
	var claims int
	_ = f.pool.QueryRow(f.ctx, "SELECT count(*) FROM benefit_claims WHERE campaign_id=$1", c2.ID).Scan(&claims)
	if claims != 0 {
		t.Fatal("failed claim persisted")
	}
}

func TestCampaignRewardValidation(t *testing.T) {
	f := newFixture(t)
	a := f.user(identity.RoleSystemAdmin)
	for _, c := range []Campaign{
		{RewardType: "lottery", Reward: &CampaignReward{Quantity: 0}},
		{RewardType: "lottery", Reward: &CampaignReward{Quantity: 101}},
		{RewardType: "lottery", Reward: &CampaignReward{Quantity: 1, Kind: "weekly"}},
		{RewardType: "reset_card", Reward: &CampaignReward{Quantity: 1, Kind: "weekly", ValidDays: -1}},
		{RewardType: "reset_card", Reward: &CampaignReward{Quantity: 1, Kind: "invalid"}},
		{RewardType: "reset_card", Reward: &CampaignReward{Quantity: 1, Kind: "weekly"}, Price: "1"},
	} {
		c.Title = "Invalid"
		c.Status = "active"
		c.Stock = -1
		c.PerUserLimit = 1
		if _, err := f.svc.SaveCampaign(f.ctx, a, uuid.Nil, c, RequestMeta{}); err == nil {
			t.Fatalf("accepted %+v", c)
		}
	}
}

func TestCampaignBundleAllRewardsRetryAndRollback(t *testing.T) {
	f := newFixture(t)
	u := f.user(identity.RoleUser)
	p := campaignPlan(t, f, 1, true)
	rewards := []CampaignRewardItem{
		{Type: "balance", Amount: "6.66"},
		{Type: "reset_card", Quantity: 2, Kind: "weekly", ValidDays: 7},
		{Type: "lottery", Quantity: 5},
		{Type: "subscription", PlanID: &p.ID},
	}
	c := newCampaign(t, f, Campaign{Rewards: rewards, Stock: 5, PerUserLimit: 5})
	if c.RewardType != "bundle" || len(c.Rewards) != 4 {
		t.Fatalf("bundle=%+v", c)
	}
	key := uuid.New()
	out, err := f.svc.ClaimCampaign(f.ctx, u, c.ID, key, 0, RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	retry, err := f.svc.ClaimCampaign(f.ctx, u, c.ID, key, 0, RequestMeta{})
	if err != nil || out.ID != retry.ID || len(retry.Rewards) != 4 {
		t.Fatalf("retry=%+v %v", retry, err)
	}
	if f.wallet(u.ID).Balance != amt("6.66") {
		t.Fatal("wallet not granted exactly once")
	}
	status, err := f.svc.Lottery(f.ctx, u.ID)
	if err != nil || status.Available != 5 {
		t.Fatalf("lottery=%+v %v", status, err)
	}
	var cards, claims int
	_ = f.pool.QueryRow(f.ctx, "SELECT count(*) FROM reset_cards WHERE user_id=$1", u.ID).Scan(&cards)
	if cards != 2 {
		t.Fatalf("cards=%d", cards)
	}
	// Plan stock is now zero. The whole second bundle must fail without changes.
	if _, err = f.svc.ClaimCampaign(f.ctx, u, c.ID, uuid.New(), 0, RequestMeta{}); err == nil {
		t.Fatal("sold out bundle accepted")
	}
	if f.wallet(u.ID).Balance != amt("6.66") {
		t.Fatal("partial balance grant")
	}
	status, _ = f.svc.Lottery(f.ctx, u.ID)
	if status.Available != 5 {
		t.Fatal("partial lottery grant")
	}
	_ = f.pool.QueryRow(f.ctx, "SELECT count(*) FROM reset_cards WHERE user_id=$1", u.ID).Scan(&cards)
	_ = f.pool.QueryRow(f.ctx, "SELECT count(*) FROM benefit_claims WHERE campaign_id=$1", c.ID).Scan(&claims)
	var stock int
	_ = f.pool.QueryRow(f.ctx, "SELECT stock FROM benefit_campaigns WHERE id=$1", c.ID).Scan(&stock)
	if cards != 2 || claims != 1 || stock != 4 {
		t.Fatalf("rollback: cards=%d claims=%d stock=%d", cards, claims, stock)
	}
	c.Rewards[2].Quantity = 10
	if _, err = f.svc.SaveCampaign(f.ctx, f.user(identity.RoleSystemAdmin), c.ID, *c, RequestMeta{}); err == nil {
		t.Fatal("claimed bundle reward changed")
	}
}

func TestCampaignPaidBundleChargesPriceOnce(t *testing.T) {
	f := newFixture(t)
	u := f.user(identity.RoleUser)
	f.credit(u.ID, "10")
	p1, p2 := campaignPlan(t, f, 1, true), campaignPlan(t, f, 1, true)
	c := newCampaign(t, f, Campaign{Rewards: []CampaignRewardItem{{Type: "subscription", PlanID: &p1.ID}, {Type: "subscription", PlanID: &p2.ID}, {Type: "balance", Amount: "1"}, {Type: "lottery", Quantity: 2}}, Price: "5.01", Stock: 1})
	if _, err := f.svc.ClaimCampaign(f.ctx, u, c.ID, uuid.New(), amt("5.01"), RequestMeta{}); err != nil {
		t.Fatal(err)
	}
	if f.wallet(u.ID).Balance != amt("5.99") {
		t.Fatalf("charged incorrectly: %+v", f.wallet(u.ID))
	}
	var purchases int
	var charged int64
	if err := f.pool.QueryRow(f.ctx, "SELECT count(*),sum(price_nano) FROM plan_purchases WHERE user_id=$1", u.ID).Scan(&purchases, &charged); err != nil {
		t.Fatal(err)
	}
	if purchases != 2 || charged != int64(amt("5.01")) {
		t.Fatalf("purchase attribution=%d %d", purchases, charged)
	}
}
