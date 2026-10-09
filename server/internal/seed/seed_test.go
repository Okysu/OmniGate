package seed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/platform/db"
	"omnigate/internal/platform/db/dbtest"
	"omnigate/internal/pricing"
	"omnigate/internal/subscription"
)

const catalogJSON = `{
  "currency": "USD",
  "prices": [
    {"kind": "sell", "model": "gpt-x", "inputPerM": "2.5", "outputPerM": "10", "cacheReadPerM": "1.25", "cacheWritePerM": "0",
     "perRequest": "0", "perImage": "0", "imageInputPerM": null, "audioInputPerM": "40", "audioOutputPerM": null,
     "perMinute": "0.006", "perMCharacters": "0", "schedule": null, "scheduleTimezone": "Asia/Shanghai", "channelName": null},
    {"kind": "sell", "model": "claude-y", "inputPerM": "3", "outputPerM": "15",
     "schedule": [{"days": [], "start": "00:00", "end": "08:00", "multiplier": "0.5"}], "scheduleTimezone": "UTC"},
    {"kind": "cost", "model": "gpt-x", "inputPerM": "2", "outputPerM": "8", "channelName": "Upstream A"}
  ],
  "modelInfo": [
    {"model": "gpt-x", "displayName": "GPT X", "description": "通用模型", "vendor": "OpenAI", "tags": ["chat", "vision"],
     "contextWindow": 128000, "maxOutput": 16384, "capabilities": {"vision": true, "tools": true}, "hidden": false, "sortOrder": 1},
    {"model": "claude-y", "displayName": "Claude Y", "vendor": "Anthropic", "sortOrder": 2}
  ],
  "plans": [
    {"name": "Go 启航者", "description": "轻量体验", "listPrice": "3", "duration": "30d", "models": [], "stackable": false, "status": "active",
     "rules": [
       {"id": "5h", "label": "5 小时滚动限额", "meter": "charge", "window": {"kind": "rolling", "duration": "5h"}, "limit": "6"},
       {"id": "weekly", "label": "每周滚动限额", "meter": "charge", "window": {"kind": "rolling", "duration": "7d"}, "limit": "12"},
       {"id": "monthly", "label": "月度总额度", "meter": "charge", "window": {"kind": "period", "every": "30d"}, "limit": "24"}
     ]},
    {"name": "Plus 进阶者", "listPrice": "10", "duration": "30d",
     "rules": [{"id": "monthly", "label": "月度总额度", "meter": "charge", "window": {"kind": "period", "every": "30d"}, "limit": "80"}]}
  ]
}`

type fixture struct {
	t    *testing.T
	ctx  context.Context
	pool *db.DB
	svc  Services
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := dbtest.Open(t)
	f := &fixture{t: t, ctx: context.Background(), pool: pool, svc: NewServices(pool, slog.New(slog.DiscardHandler))}
	f.exec(`INSERT INTO system_settings (key, value) VALUES ('billing.currency', $1)`, []byte(`{"code":"USD","symbol":"$","decimals":2}`))
	owner := uuid.Must(uuid.NewV7())
	f.exec(`INSERT INTO users (id, display_name, role) VALUES ($1, 'owner', 'system_admin')`, owner)
	f.exec(`INSERT INTO channels (id, owner_id, name, type, base_url) VALUES ($1, $2, 'Upstream A', 'openai', 'https://a.example')`,
		uuid.Must(uuid.NewV7()), owner)
	return f
}

func (f *fixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.pool.Exec(f.ctx, sql, args...); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) count(sql string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(f.ctx, sql, args...).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func parse(t *testing.T, s string) *Catalog {
	t.Helper()
	c, err := Parse(strings.NewReader(s))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (f *fixture) apply(c *Catalog, dryRun bool) *Report {
	f.t.Helper()
	rep, err := Apply(f.ctx, f.svc, c, dryRun)
	if err != nil {
		f.t.Fatal(err)
	}
	return rep
}

func actions(rep *Report) map[string]Action {
	out := map[string]Action{}
	for _, c := range rep.Changes {
		out[c.Section+":"+c.Key] = c.Action
	}
	return out
}

func wantActions(t *testing.T, rep *Report, want map[string]Action) {
	t.Helper()
	got := actions(rep)
	if len(got) != len(want) {
		t.Fatalf("changes = %v, want %v", got, want)
	}
	for k, a := range want {
		if got[k] != a {
			t.Errorf("%s: action %q, want %q (all: %v)", k, got[k], a, got)
		}
	}
}

func (f *fixture) catalogRows() (prices, infos, plans, audits int) {
	return f.count(`SELECT count(*) FROM prices`), f.count(`SELECT count(*) FROM model_info`),
		f.count(`SELECT count(*) FROM plans`), f.count(`SELECT count(*) FROM audit_logs`)
}

func TestApplyIdempotentAndIncremental(t *testing.T) {
	f := newFixture(t)
	rep := f.apply(parse(t, catalogJSON), false)
	wantActions(t, rep, map[string]Action{
		"price:sell gpt-x": ActionCreate, "price:sell claude-y": ActionCreate, "price:cost gpt-x @ Upstream A": ActionCreate,
		"model-info:gpt-x": ActionCreate, "model-info:claude-y": ActionCreate,
		"plan:Go 启航者": ActionCreate, "plan:Plus 进阶者": ActionCreate,
	})
	if rep.Applied() != 7 {
		t.Fatalf("applied %d", rep.Applied())
	}
	var out bytes.Buffer
	rep.Print(&out)
	if !strings.Contains(out.String(), "已写入 7 项变更") {
		t.Fatalf("summary:\n%s", out.String())
	}
	p, i, pl, a := f.catalogRows()
	if p != 3 || i != 2 || pl != 2 || a != 7 {
		t.Fatalf("rows prices=%d info=%d plans=%d audit=%d", p, i, pl, a)
	}

	// Stored values went through the services.
	sell, err := f.svc.Prices.Lookup(f.ctx, pricing.KindSell, "claude-y", nil, time.Now().UTC())
	if err != nil || sell == nil || sell.InputPerM.String() != "3" || len(sell.Schedule) != 1 || sell.ScheduleTimezone != "UTC" {
		t.Fatalf("claude-y sell = %+v, %v", sell, err)
	}
	plans, _, err := f.svc.Plans.ListPlans(f.ctx, "", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	// New plans are created last to first so the catalog (newest first) shows
	// them in file order.
	if len(plans) != 2 || plans[0].Name != "Go 启航者" || plans[1].Name != "Plus 进阶者" {
		t.Fatalf("catalog order = %v, %v", plans[0].Name, plans[1].Name)
	}
	var goPlan *subscription.Plan
	for _, p := range plans {
		if p.Name == "Go 启航者" {
			goPlan = p
		}
	}
	if goPlan == nil || goPlan.ListPrice.String() != "3" || len(goPlan.Rules) != 3 || goPlan.Rules[1].Window.Duration != "7d" {
		t.Fatalf("plan = %+v", goPlan)
	}

	// A subscription to the plan keeps its snapshot through later updates.
	user := uuid.Must(uuid.NewV7())
	f.exec(`INSERT INTO users (id, display_name, role) VALUES ($1, 'buyer', 'user')`, user)
	sub, _, err := f.svc.Plans.Grant(f.ctx, f.pool, user, goPlan.ID, 1, subscription.SourceAdmin, "")
	if err != nil {
		t.Fatal(err)
	}

	// Re-applying is a no-op.
	rep = f.apply(parse(t, catalogJSON), false)
	for _, c := range rep.Changes {
		if c.Action != ActionUnchanged {
			t.Errorf("re-apply: %s %s = %s %v", c.Section, c.Key, c.Action, c.Details)
		}
	}
	if p2, i2, pl2, a2 := f.catalogRows(); p2 != p || i2 != i || pl2 != pl || a2 != a {
		t.Fatalf("re-apply wrote rows: %d %d %d %d", p2, i2, pl2, a2)
	}
	out.Reset()
	rep.Print(&out)
	if !strings.Contains(out.String(), "无需变更") {
		t.Fatalf("summary:\n%s", out.String())
	}

	// Change one price and one plan: only those are written.
	changed := strings.Replace(catalogJSON, `"inputPerM": "3", "outputPerM": "15"`, `"inputPerM": "3.3", "outputPerM": "15"`, 1)
	changed = strings.Replace(changed, `"limit": "12"`, `"limit": "14"`, 1)
	rep = f.apply(parse(t, changed), false)
	wantActions(t, rep, map[string]Action{
		"price:sell gpt-x": ActionUnchanged, "price:sell claude-y": ActionNewVersion, "price:cost gpt-x @ Upstream A": ActionUnchanged,
		"model-info:gpt-x": ActionUnchanged, "model-info:claude-y": ActionUnchanged,
		"plan:Go 启航者": ActionUpdate, "plan:Plus 进阶者": ActionUnchanged,
	})
	for _, c := range rep.Changes {
		switch c.Key {
		case "sell claude-y":
			if !strings.Contains(strings.Join(c.Details, " "), "inputPerM 3 → 3.3") {
				t.Errorf("price diff = %v", c.Details)
			}
		case "Go 启航者":
			d := strings.Join(c.Details, " ")
			if !strings.Contains(d, "修改规则 weekly") || !strings.Contains(d, "≤ 14") || !strings.Contains(d, "1 个有效订阅") {
				t.Errorf("plan diff = %v", c.Details)
			}
		}
	}
	if p3, _, pl3, a3 := f.catalogRows(); p3 != p+1 || pl3 != pl || a3 != a+2 {
		t.Fatalf("incremental apply: prices %d plans %d audit %d", p3, pl3, a3)
	}
	cur, _ := f.svc.Plans.GetPlan(f.ctx, goPlan.ID)
	if cur.Version != goPlan.Version+1 || cur.Rules[1].Limit != "14" {
		t.Fatalf("updated plan = %+v", cur)
	}
	views, err := f.svc.Plans.ListMine(f.ctx, user)
	if err != nil || len(views) != 1 {
		t.Fatalf("subscriptions %v %v", views, err)
	}
	var snap []subscription.Rule
	if err := f.pool.QueryRow(f.ctx, `SELECT rules FROM subscriptions WHERE id = $1`, sub.ID).Scan(&snap); err != nil {
		t.Fatal(err)
	}
	if len(snap) != 3 || snap[1].Limit != "12" {
		t.Fatalf("subscription snapshot changed: %+v", snap)
	}

	// Model info update by model.
	changed = strings.Replace(changed, `"displayName": "Claude Y"`, `"displayName": "Claude Y+"`, 1)
	rep = f.apply(parse(t, changed), false)
	if actions(rep)["model-info:claude-y"] != ActionUpdate || rep.Applied() != 1 {
		t.Fatalf("model info update: %v", actions(rep))
	}
	all, _ := f.svc.Info.All(f.ctx)
	if all["claude-y"].DisplayName != "Claude Y+" || all["claude-y"].Version != 2 {
		t.Fatalf("model info = %+v", all["claude-y"])
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	f := newFixture(t)
	p, i, pl, a := f.catalogRows()
	rep := f.apply(parse(t, catalogJSON), true)
	if rep.Pending() != 7 || rep.Applied() != 0 {
		t.Fatalf("pending %d applied %d", rep.Pending(), rep.Applied())
	}
	if p2, i2, pl2, a2 := f.catalogRows(); p2 != p || i2 != i || pl2 != pl || a2 != a {
		t.Fatalf("dry run wrote rows")
	}
	var out bytes.Buffer
	rep.Print(&out)
	if !strings.Contains(out.String(), "试运行：未写入任何数据（7 项将会变更）") || !strings.Contains(out.String(), "新建（试运行）") {
		t.Fatalf("output:\n%s", out.String())
	}
	// After a real apply, a dry run of a modified catalog reports only the change.
	f.apply(parse(t, catalogJSON), false)
	_, _, _, a = f.catalogRows()
	rep = f.apply(parse(t, strings.Replace(catalogJSON, `"limit": "80"`, `"limit": "90"`, 1)), true)
	if rep.Pending() != 1 || actions(rep)["plan:Plus 进阶者"] != ActionUpdate {
		t.Fatalf("dry run: %v", actions(rep))
	}
	if _, _, _, a2 := f.catalogRows(); a2 != a {
		t.Fatal("dry run audited")
	}
}

func TestAuditEntries(t *testing.T) {
	f := newFixture(t)
	rep := f.apply(parse(t, catalogJSON), false)
	rows, err := f.pool.Query(f.ctx, `SELECT actor_id, actor_name, action, request_id, metadata FROM audit_logs ORDER BY created_at`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[string]int{}
	for rows.Next() {
		var actorID *uuid.UUID
		var name, action, reqID string
		var md map[string]any
		if err := rows.Scan(&actorID, &name, &action, &reqID, &md); err != nil {
			t.Fatal(err)
		}
		if actorID != nil || name != ActorName || reqID != rep.RequestID || md["source"] != "seed" {
			t.Errorf("audit %s: actor %v %q request %q metadata %v", action, actorID, name, reqID, md)
		}
		seen[action]++
	}
	if seen["price.create"] != 3 || seen["model_info.update"] != 2 || seen["plan.create"] != 2 {
		t.Fatalf("audit actions = %v", seen)
	}
	// System actor: no creator rows.
	if n := f.count(`SELECT count(*) FROM plans WHERE created_by IS NOT NULL`); n != 0 {
		t.Fatalf("plans with creator: %d", n)
	}
	if n := f.count(`SELECT count(*) FROM prices WHERE created_by IS NOT NULL`); n != 0 {
		t.Fatalf("prices with creator: %d", n)
	}
	if n := f.count(`SELECT count(*) FROM model_info WHERE updated_by IS NOT NULL`); n != 0 {
		t.Fatalf("model info with updater: %d", n)
	}
}

func TestInvalidCatalog(t *testing.T) {
	f := newFixture(t)
	bad := `{
  "currency": "USD",
  "prices": [
    {"kind": "sell", "model": "m", "inputPerM": "-1"},
    {"kind": "cost", "model": "m", "inputPerM": "1"},
    {"kind": "cost", "model": "m", "channelName": "nope"},
    {"kind": "sell", "model": "m2", "channelName": "Upstream A"},
    {"kind": "sell", "model": "m3", "schedule": [{"start": "25:00", "end": "08:00", "multiplier": "1"}]},
    {"kind": "sell", "model": "m3"}
  ],
  "modelInfo": [{"model": "x", "maxOutput": 10, "contextWindow": 5}, {"model": " y"}],
  "plans": [
    {"name": "P", "duration": "30d", "rules": [{"id": "5h", "meter": "charge", "window": {"kind": "rolling", "duration": "40d"}, "limit": "abc"}]},
    {"name": "Q", "duration": "", "listPrice": "x", "rules": []},
    {"name": "P", "duration": "30d", "rules": [{"id": "a", "meter": "nope", "window": {"kind": "period", "every": "30d"}, "limit": "1"}]}
  ]
}`
	_, err := Apply(f.ctx, f.svc, parse(t, bad), false)
	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("err = %v", err)
	}
	msg := err.Error()
	for _, path := range []string{"prices[0].inputPerM", "prices[1].channelName", "prices[2].channelName", "prices[3].channelName",
		"prices[4].schedule", "modelInfo[0].maxOutput", "modelInfo[1].model", "plans[0].rules[0].window.duration",
		"plans[0].rules[0].limit", "plans[1].duration", "plans[1].listPrice", "plans[1].rules", "plans[2].name", "plans[2].rules[0].meter"} {
		if !strings.Contains(msg, path+": ") {
			t.Errorf("missing %s in:\n%s", path, msg)
		}
	}
	if p, i, pl, a := f.catalogRows(); p+i+pl+a != 0 {
		t.Fatalf("invalid catalog wrote rows")
	}

	// Currency mismatch.
	_, err = Apply(f.ctx, f.svc, parse(t, `{"currency": "CNY"}`), false)
	if !errors.As(err, &verr) || !strings.Contains(err.Error(), "currency: ") {
		t.Fatalf("currency: %v", err)
	}
}

func TestParseErrors(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{``, "文件为空"},
		{`{"prices": [}`, "JSON 语法错误（第 1 行"},
		{`{"foo": 1}`, `未知字段 "foo"`},
		{`{"prices": [{"model": "m", "inputPerM": 3}]}`, "prices[0]: 字段 inputPerM 类型错误"},
		{`{"plans": [{"name": "p", "rules": [{"id": "a", "limit": 5}]}]}`, "plans[0]: 字段 rules.limit 类型错误"},
		{`{"modelInfo": [{"model": "m", "capabilities": {"telepathy": true}}]}`, `modelInfo[0]: 未知字段 "telepathy"`},
		{`{"modelInfo": [null]}`, "modelInfo[0]: 不能为 null"},
		{`{} {}`, "多余内容"},
	} {
		_, err := Parse(strings.NewReader(tc.in))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Parse(%q) = %v, want %q", tc.in, err, tc.want)
		}
	}
}

func TestExportRoundTrip(t *testing.T) {
	f := newFixture(t)
	f.apply(parse(t, catalogJSON), false)
	// An archived plan is not exported.
	f.exec(`UPDATE plans SET status = 'archived' WHERE name = 'Plus 进阶者'`)
	c, err := Export(f.ctx, f.svc, ExportOptions{Prices: true, Cost: true, ModelInfo: true, Plans: true}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if c.Currency != "USD" || len(c.Prices) != 3 || len(c.ModelInfo) != 2 || len(c.Plans) != 1 {
		t.Fatalf("export = %+v", c)
	}
	if c.Prices[0].Kind != "sell" || c.Prices[2].Kind != "cost" || *c.Prices[2].ChannelName != "Upstream A" {
		t.Fatalf("prices = %+v", c.Prices)
	}
	var buf bytes.Buffer
	if err := c.Write(&buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"audioInputPerM": "40"`) || !strings.Contains(buf.String(), `"imageInputPerM": null`) {
		t.Fatalf("export json:\n%s", buf.String())
	}
	// The export applies cleanly as a no-op.
	rep := f.apply(parse(t, buf.String()), false)
	if rep.Pending() != 0 {
		t.Fatalf("re-applying the export changes: %v", actions(rep))
	}
	// Sections can be selected; sell only.
	c, err = Export(f.ctx, f.svc, ExportOptions{Prices: true}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Prices) != 2 || c.ModelInfo != nil || c.Plans != nil {
		t.Fatalf("sell-only export = %+v", c)
	}
	raw, _ := json.Marshal(c)
	if strings.Contains(string(raw), "modelInfo") {
		t.Fatalf("unselected section present: %s", raw)
	}
}
