package billing

import (
	"encoding/json"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"omnigate/internal/audit"
	"omnigate/internal/identity"
	"omnigate/internal/money"
	"omnigate/internal/subscription"
	"sync"
	"testing"
	"time"
)

func TestCampaignHTTPDefaultsPermissionsAndPrivacy(t *testing.T) {
	f := newFixture(t)
	h := newHTTP(t, f)
	admin := f.user(identity.RoleSystemAdmin)
	u := f.user(identity.RoleUser)
	body := map[string]any{"id": "", "title": "活动", "rewardType": "balance", "amount": "2", "status": "active", "conditions": map[string]any{"userIds": []string{u.ID.String()}}}
	status, result := h.do(admin, identity.RoleSystemAdmin, "POST", "/api/admin/benefits/campaigns", body)
	if status != 200 || result["stock"] != float64(-1) || result["perUserLimit"] != float64(1) {
		t.Fatalf("defaults: %d %v", status, result)
	}
	id := result["id"].(string)
	status, _ = h.do(u, identity.RoleUser, "POST", "/api/admin/benefits/campaigns", body)
	if status != 403 {
		t.Fatalf("permission=%d", status)
	}
	requestID := uuid.NewString()
	status, _ = h.do(u, identity.RoleUser, "POST", "/api/benefits/campaigns/"+id+"/claim", map[string]any{"requestId": requestID})
	if status != 200 {
		t.Fatalf("claim=%d", status)
	}
	status, _ = h.do(u, identity.RoleUser, "POST", "/api/benefits/campaigns/"+id+"/claim", map[string]any{"requestId": requestID})
	if status != 200 || f.wallet(u.ID).Balance != amt("2") {
		t.Fatal("HTTP retry credited twice")
	}
	router := chi.NewRouter()
	NewHandler(f.svc).PublicCampaignRoutes(router)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/public/benefits/"+id, nil))
	if rec.Code != 200 {
		t.Fatalf("public=%d %s", rec.Code, rec.Body.String())
	}
	var public map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &public)
	if _, ok := public["conditions"]; ok {
		t.Fatal("private eligibility lists exposed")
	}
	if len(public["requirements"].([]any)) != 1 {
		t.Fatalf("public requirements %v", public)
	}
	// Draft activities must not be publicly accessible.
	body["version"] = result["version"]
	body["status"] = "draft"
	status, _ = h.do(admin, identity.RoleSystemAdmin, "PUT", "/api/admin/benefits/campaigns/"+id, body)
	if status != 200 {
		t.Fatalf("save=%d", status)
	}
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/public/benefits/"+id, nil))
	if rec.Code != 404 {
		t.Fatal("draft publicly exposed")
	}
}

func campaignPlan(t *testing.T, f *fixture, stock int64, hidden bool) *subscription.Plan {
	t.Helper()
	svc := subscription.NewService(f.pool, audit.NewRecorder(f.pool, slog.Default()), slog.Default())
	a := f.user(identity.RoleSystemAdmin)
	p, err := svc.CreatePlan(f.ctx, subscription.Actor{ID: a.ID, Name: a.Name}, subscription.PlanInput{Name: "7天活动", Duration: "7d", Stock: &stock, Hidden: hidden, Stackable: true, ListPrice: ptrAmount("5"), Models: []string{"m"}, Rules: []subscription.Rule{{ID: "total", Meter: subscription.MeterCharge, Limit: "10", Window: subscription.Window{Kind: subscription.WindowLifetime}}}}, subscription.RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func ptrAmount(s string) *money.Amount { v := amt(s); return &v }

func newCampaign(t *testing.T, f *fixture, c Campaign) *Campaign {
	t.Helper()
	if c.Title == "" {
		c.Title = "福利"
	}
	if c.Status == "" {
		c.Status = "active"
	}
	if c.PerUserLimit == 0 {
		c.PerUserLimit = 1
	}
	a := f.user(identity.RoleSystemAdmin)
	out, err := f.svc.SaveCampaign(f.ctx, a, uuid.Nil, c, RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCampaignClaimIdempotencyAndConditions(t *testing.T) {
	f := newFixture(t)
	u := f.user(identity.RoleUser)
	c := newCampaign(t, f, Campaign{RewardType: "balance", Amount: "9.99", Stock: 2, PerUserLimit: 2, Conditions: CampaignConditions{UserIDs: []uuid.UUID{u.ID}}})
	denied := f.user(identity.RoleUser)
	if _, err := f.svc.ClaimCampaign(f.ctx, denied, c.ID, uuid.New(), 0, RequestMeta{}); errCode(err) != "campaign_not_eligible" {
		t.Fatalf("eligibility: %v", err)
	}
	key := uuid.New()
	a, err := f.svc.ClaimCampaign(f.ctx, u, c.ID, key, 0, RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.svc.ClaimCampaign(f.ctx, u, c.ID, key, 0, RequestMeta{})
	if err != nil || a.ID != b.ID {
		t.Fatalf("idempotency %v", err)
	}
	if got := f.wallet(u.ID).Balance.String(); got != "9.99" {
		t.Fatal(got)
	}
	if _, err = f.svc.ClaimCampaign(f.ctx, u, c.ID, uuid.New(), 0, RequestMeta{}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.ClaimCampaign(f.ctx, u, c.ID, uuid.New(), 0, RequestMeta{}); err == nil {
		t.Fatal("third claim accepted")
	}
	f.checkLedger(u.ID)
}

func TestCampaignPaidSubscriptionAndRollback(t *testing.T) {
	f := newFixture(t)
	u := f.user(identity.RoleUser)
	p := campaignPlan(t, f, 1, true)
	c := newCampaign(t, f, Campaign{RewardType: "subscription", PlanID: &p.ID, Price: "3", Stock: 5})
	if _, err := f.svc.ClaimCampaign(f.ctx, u, c.ID, uuid.New(), amt("3"), RequestMeta{}); errCode(err) != "insufficient_balance" {
		t.Fatalf("balance: %v", err)
	}
	var stock int64
	_ = f.pool.QueryRow(f.ctx, `SELECT stock FROM plans WHERE id=$1`, p.ID).Scan(&stock)
	if stock != 1 {
		t.Fatal("failed payment consumed stock")
	}
	f.credit(u.ID, "10")
	if _, err := f.svc.ClaimCampaign(f.ctx, u, c.ID, uuid.New(), amt("2"), RequestMeta{}); errCode(err) != "price_changed" {
		t.Fatal(err)
	}
	claim, err := f.svc.ClaimCampaign(f.ctx, u, c.ID, uuid.New(), amt("3"), RequestMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if claim.SubscriptionID == nil || f.wallet(u.ID).Balance.String() != "7" {
		t.Fatalf("claim %+v", claim)
	}
	sub, err := subscription.GetSubTx(f.ctx, f.pool, *claim.SubscriptionID, false)
	if err != nil {
		t.Fatal(err)
	}
	if sub.EndsAt.Sub(sub.StartsAt) != 7*24*time.Hour {
		t.Fatal("wrong expiry")
	}
	if _, err = f.svc.Purchase(f.ctx, u, PurchaseInput{PlanID: p.ID, ExpectedPrice: amt("5")}, RequestMeta{}); errCode(err) != "plan_not_for_sale" {
		t.Fatalf("hidden bypass: %v", err)
	}
	options, err := f.svc.PurchaseOptions(f.ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range options.Plans {
		if o.Plan.ID == p.ID {
			t.Fatal("hidden plan in catalog")
		}
	}
	other := f.user(identity.RoleUser)
	f.credit(other.ID, "10")
	if _, err = f.svc.ClaimCampaign(f.ctx, other, c.ID, uuid.New(), amt("3"), RequestMeta{}); errCode(err) != "plan_sold_out" {
		t.Fatalf("stock: %v", err)
	}
	if f.wallet(other.ID).Balance.String() != "10" {
		t.Fatal("failed reward charged wallet")
	}
	var purchases int
	_ = f.pool.QueryRow(f.ctx, `SELECT count(*) FROM plan_purchases WHERE user_id=$1`, u.ID).Scan(&purchases)
	if purchases != 1 {
		t.Fatal("missing purchase record")
	}
	f.checkLedger(u.ID)
}

func TestCampaignConcurrentStockAndPurchaseStock(t *testing.T) {
	f := newFixture(t)
	c := newCampaign(t, f, Campaign{RewardType: "balance", Amount: "1", Stock: 1})
	actors := make([]Actor, 8)
	for i := range actors {
		actors[i] = f.user(identity.RoleUser)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	success := 0
	for _, a := range actors {
		wg.Go(func() {
			_, err := f.svc.ClaimCampaign(f.ctx, a, c.ID, uuid.New(), 0, RequestMeta{})
			if err == nil {
				mu.Lock()
				success++
				mu.Unlock()
			} else if errCode(err) != "campaign_sold_out" {
				t.Errorf("unexpected %v", err)
			}
		})
	}
	wg.Wait()
	if success != 1 {
		t.Fatalf("claims=%d", success)
	}
	p := campaignPlan(t, f, 1, false)
	u := actors[0]
	f.credit(u.ID, "20")
	if _, err := f.svc.Purchase(f.ctx, u, PurchaseInput{PlanID: p.ID, ExpectedPrice: amt("5")}, RequestMeta{}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Purchase(f.ctx, u, PurchaseInput{PlanID: p.ID, ExpectedPrice: amt("5")}, RequestMeta{}); errCode(err) != "plan_sold_out" {
		t.Fatal(err)
	}
	if f.wallet(u.ID).Balance > amt("16") {
		t.Fatal("unexpected wallet")
	}
}

func TestCampaignWindowPurchaseAndSpendConditions(t *testing.T) {
	f := newFixture(t)
	u := f.user(identity.RoleUser)
	future := time.Now().Add(time.Hour)
	c := newCampaign(t, f, Campaign{RewardType: "balance", Amount: "1", Stock: -1, StartsAt: &future})
	if _, err := f.svc.ClaimCampaign(f.ctx, u, c.ID, uuid.New(), 0, RequestMeta{}); errCode(err) != "campaign_unavailable" {
		t.Fatal(err)
	}
	c = newCampaign(t, f, Campaign{RewardType: "balance", Amount: "1", Stock: -1, Conditions: CampaignConditions{MinSpend: "10", RequirePurchase: true}})
	if _, err := f.svc.ClaimCampaign(f.ctx, u, c.ID, uuid.New(), 0, RequestMeta{}); errCode(err) != "campaign_not_eligible" {
		t.Fatal(err)
	}
	p := campaignPlan(t, f, -1, false)
	f.credit(u.ID, "10")
	if _, err := f.svc.Purchase(f.ctx, u, PurchaseInput{PlanID: p.ID, ExpectedPrice: amt("5")}, RequestMeta{}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ClaimCampaign(f.ctx, u, c.ID, uuid.New(), 0, RequestMeta{}); errCode(err) != "campaign_not_eligible" {
		t.Fatal("purchase counted as model spend")
	}
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO lottery_days(user_id,day,spent_nano,earned) VALUES($1,'2026-11-11',10000000000,false)`, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ClaimCampaign(f.ctx, u, c.ID, uuid.New(), 0, RequestMeta{}); err != nil {
		t.Fatal(err)
	}
}
