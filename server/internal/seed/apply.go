package seed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"omnigate/internal/apperr"
	"omnigate/internal/audit"
	"omnigate/internal/authz"
	"omnigate/internal/identity"
	"omnigate/internal/money"
	"omnigate/internal/platform/db"
	"omnigate/internal/plaza"
	"omnigate/internal/pricing"
	"omnigate/internal/subscription"
)

// ActorName is the audit actor of seeded changes (the system actor: no user).
const ActorName = "seed"

// Services are the catalog services seeding goes through.
type Services struct {
	Pool   *db.DB
	Prices *pricing.Service
	Info   *plaza.InfoService
	Plans  *subscription.Service
}

// NewServices builds the catalog services on pool. Plans whose rules use
// custom (plugin) meters need Plans.CustomMeters (app.SeedServices sets it).
func NewServices(pool *db.DB, log *slog.Logger) Services {
	rec := audit.NewRecorder(pool, log)
	return Services{Pool: pool, Prices: pricing.NewService(pool, rec), Info: plaza.NewInfoService(pool, rec),
		Plans: subscription.NewService(pool, rec, log)}
}

// Action is what seeding does with one catalog entry.
type Action string

const (
	ActionCreate     Action = "create"      // new price / model info / plan
	ActionNewVersion Action = "new-version" // price differs: new version effective now
	ActionUpdate     Action = "update"      // model info / plan differs: updated in place
	ActionUnchanged  Action = "unchanged"
)

// Change is the outcome of one catalog entry.
type Change struct {
	Section string // "price", "model-info" or "plan"
	Key     string // e.g. "sell gpt-4o", "gpt-4o", "Pro 专业者"
	Action  Action
	Details []string
	Applied bool // written (never in a dry run)

	price       *pricing.CreateInput
	infoModel   string
	info        *plaza.Input
	plan        *subscription.PlanInput
	planID      uuid.UUID
	planVersion int
}

// Report is the result of a seed run.
type Report struct {
	DryRun    bool
	RequestID string // audit request id of every entry written by this run
	Changes   []Change
	Warnings  []string
}

// Counts returns the number of changes per section and action.
func (r *Report) Counts() map[string]map[Action]int {
	out := map[string]map[Action]int{}
	for _, c := range r.Changes {
		if out[c.Section] == nil {
			out[c.Section] = map[Action]int{}
		}
		out[c.Section][c.Action]++
	}
	return out
}

// Pending reports how many entries would change.
func (r *Report) Pending() int {
	n := 0
	for _, c := range r.Changes {
		if c.Action != ActionUnchanged {
			n++
		}
	}
	return n
}

// Applied reports how many entries were written.
func (r *Report) Applied() int {
	n := 0
	for _, c := range r.Changes {
		if c.Applied {
			n++
		}
	}
	return n
}

// Apply validates the catalog, compares it with the database and, unless
// dryRun, writes the differences through the services. A *ValidationError
// means nothing was written. On a write error the report lists what was
// applied before it; running the same catalog again resumes (seeding is
// idempotent).
func Apply(ctx context.Context, svc Services, c *Catalog, dryRun bool) (*Report, error) {
	rep := &Report{DryRun: dryRun, RequestID: "seed-" + uuid.Must(uuid.NewV7()).String()}
	if err := svc.plan(ctx, c, rep); err != nil {
		return rep, err
	}
	if dryRun {
		return rep, nil
	}
	ctx = audit.WithMetadata(ctx, map[string]any{"source": "seed"})
	principal := &authz.Principal{UserID: uuid.Nil, Name: ActorName, Role: identity.RoleSystemAdmin}
	for _, i := range executionOrder(rep.Changes) {
		ch := &rep.Changes[i]
		var err error
		switch {
		case ch.Action == ActionUnchanged:
			continue
		case ch.price != nil:
			_, err = svc.Prices.Create(ctx, principal, *ch.price, "", rep.RequestID)
		case ch.info != nil:
			_, _, err = svc.Info.Put(ctx, plaza.Actor{Name: ActorName}, ch.infoModel, *ch.info,
				plaza.RequestMeta{RequestID: rep.RequestID})
		case ch.plan != nil && ch.Action == ActionCreate:
			_, err = svc.Plans.CreatePlan(ctx, subscription.Actor{Name: ActorName}, *ch.plan,
				subscription.RequestMeta{RequestID: rep.RequestID})
		case ch.plan != nil:
			in := ch.plan
			_, err = svc.Plans.UpdatePlan(ctx, subscription.Actor{Name: ActorName}, ch.planID, subscription.PlanPatch{
				Name: &in.Name, Description: &in.Description, SetListPrice: true, ListPrice: in.ListPrice,
				Duration: &in.Duration, Models: &in.Models, Rules: &in.Rules, Stackable: &in.Stackable, Status: &in.Status,
				Version: ch.planVersion}, subscription.RequestMeta{RequestID: rep.RequestID})
		}
		if err != nil {
			return rep, fmt.Errorf("%s %q: %w", ch.Section, ch.Key, describeServiceError(err))
		}
		ch.Applied = true
	}
	return rep, nil
}

// executionOrder lists the changes in file order, except that new plans are
// created last to first: the plan catalog lists the newest plan first, so
// the first plan of the file is shown first.
func executionOrder(changes []Change) []int {
	order, creates := []int{}, []int{}
	for i, c := range changes {
		if c.Section == "plan" && c.Action == ActionCreate {
			creates = append(creates, i)
		} else {
			order = append(order, i)
		}
	}
	slices.Reverse(creates)
	return append(order, creates...)
}

// describeServiceError renders an application error with its details.
func describeServiceError(err error) error {
	var ae *apperr.Error
	if !errors.As(err, &ae) || len(ae.Details) == 0 {
		return err
	}
	parts := []string{}
	for k, v := range ae.Details {
		parts = append(parts, fmt.Sprintf("%s: %v", k, v))
	}
	slices.Sort(parts)
	return fmt.Errorf("%s（%s）", ae.Message, strings.Join(parts, "；"))
}

// plan validates everything and fills rep.Changes; no writes.
func (s Services) plan(ctx context.Context, c *Catalog, rep *Report) error {
	var ps problems
	if c.Currency != "" {
		stored, err := StoredCurrency(ctx, s.Pool)
		if err != nil {
			return err
		}
		switch {
		case stored == "":
			rep.Warnings = append(rep.Warnings, fmt.Sprintf(
				"目标数据库尚未记录结算币种（服务从未启动过）；文件声明的币种是 %s，首次启动时请确认 OMNIGATE_CURRENCY=%s", c.Currency, c.Currency))
		case stored != c.Currency:
			ps.add("currency", "文件中的金额以 %s 计价，但目标部署的结算币种是 %s（金额不会换算）", c.Currency, stored)
		}
	}
	now := time.Now().UTC()
	if err := s.planPrices(ctx, c.Prices, now, &ps, rep); err != nil {
		return err
	}
	if err := s.planModelInfo(ctx, c.ModelInfo, &ps, rep); err != nil {
		return err
	}
	if err := s.planPlans(ctx, c.Plans, &ps, rep); err != nil {
		return err
	}
	return ps.err()
}

// StoredCurrency returns the deployment's settlement currency code ("" before
// the first start).
func StoredCurrency(ctx context.Context, pool *db.DB) (string, error) {
	var cur struct {
		Code string `json:"code"`
	}
	err := pool.QueryRow(ctx, `SELECT value FROM system_settings WHERE key = 'billing.currency'`).Scan(&cur)
	if db.IsNoRows(err) {
		return "", nil
	}
	return cur.Code, err
}

// channelsByName maps channel names to their ids (names are not unique).
func channelsByName(ctx context.Context, pool *db.DB) (map[string][]uuid.UUID, map[uuid.UUID]string, error) {
	rows, err := pool.Query(ctx, `SELECT id, name FROM channels ORDER BY created_at, id`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	byName, byID := map[string][]uuid.UUID{}, map[uuid.UUID]string{}
	for rows.Next() {
		var id uuid.UUID
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, nil, err
		}
		byName[name] = append(byName[name], id)
		byID[id] = name
	}
	return byName, byID, rows.Err()
}

// validationDetails returns the field details of a service validation error
// (nil for other errors, which abort the run).
func validationDetails(err error) map[string]any {
	var ae *apperr.Error
	if errors.As(err, &ae) && ae.Kind == apperr.KindValidation {
		if ae.Details == nil {
			return map[string]any{"": ae.Message}
		}
		return ae.Details
	}
	return nil
}

// ---- prices ----

func (s Services) planPrices(ctx context.Context, entries []PriceEntry, now time.Time, ps *problems, rep *Report) error {
	if len(entries) == 0 {
		return nil
	}
	byName, _, err := channelsByName(ctx, s.Pool)
	if err != nil {
		return err
	}
	seen := map[string]int{}
	for i, e := range entries {
		path := fmt.Sprintf("prices[%d]", i)
		before := len(*ps)
		kind := strings.TrimSpace(e.Kind)
		if kind == "" {
			kind = pricing.KindSell
		}
		var chID *uuid.UUID
		chName := ""
		if e.ChannelName != nil {
			chName = strings.TrimSpace(*e.ChannelName)
		}
		switch {
		case chName != "" && kind == pricing.KindSell:
			ps.add(path+".channelName", "sell 价格不能指定渠道（channelName 只用于 cost 价格）")
		case chName != "":
			switch ids := byName[chName]; len(ids) {
			case 0:
				ps.add(path+".channelName", "渠道 %q 不存在", chName)
			case 1:
				chID = &ids[0]
			default:
				ps.add(path+".channelName", "有 %d 个名为 %q 的渠道，无法确定是哪一个（请先重命名）", len(ids), chName)
			}
		case kind == pricing.KindCost:
			ps.add(path+".channelName", "cost 价格必须用 channelName 指定渠道")
		}
		in := pricing.CreateInput{Kind: kind, Model: e.Model, ChannelID: chID, InputPerM: e.InputPerM, OutputPerM: e.OutputPerM,
			CacheReadPerM: e.CacheReadPerM, CacheWritePerM: e.CacheWritePerM, PerRequest: e.PerRequest,
			PerImage: &e.PerImage, ImageInputPerM: e.ImageInputPerM, AudioInputPerM: e.AudioInputPerM,
			AudioOutputPerM: e.AudioOutputPerM, PerMinute: &e.PerMinute, PerMCharacters: &e.PerMCharacters,
			Schedule: e.Schedule, ScheduleTimezone: &e.ScheduleTimezone, Tiers: e.Tiers}
		check := in
		if kind == pricing.KindCost && chID == nil {
			check.ChannelID = &uuid.Nil // reported as channelName above
		}
		next, err := pricing.Prepare(check)
		if err != nil {
			d := validationDetails(err)
			if d == nil {
				return err
			}
			ps.addDetails(path, d, map[string]string{"channelId": "channelName"})
		}
		key := kind + " " + e.Model
		if chName != "" {
			key += " @ " + chName
		}
		if j, dup := seen[key]; dup {
			ps.add(path, "与 prices[%d] 重复（相同的 kind、model 与渠道）", j)
		}
		seen[key] = i
		if len(*ps) > before {
			continue
		}
		cur, err := s.Prices.Lookup(ctx, kind, e.Model, chID, now)
		if err != nil {
			return err
		}
		ch := Change{Section: "price", Key: key, price: &in}
		switch {
		case cur == nil:
			ch.Action, ch.Details = ActionCreate, []string{strings.Join(priceSummary(next), "，")}
		case pricing.SamePrice(cur, next):
			ch.Action, ch.price = ActionUnchanged, nil
		default:
			ch.Action, ch.Details = ActionNewVersion, []string{strings.Join(priceDiff(cur, next), "，")}
		}
		upcoming, err := s.Prices.Upcoming(ctx, kind, e.Model, chID, now)
		if err != nil {
			return err
		}
		if len(upcoming) > 0 {
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("价格 %s 已有 %d 个将来生效的版本（最早 %s），届时会取代当前价格和本次写入的版本",
				key, len(upcoming), upcoming[0].EffectiveAt.UTC().Format("2006-01-02 15:04 UTC")))
		}
		rep.Changes = append(rep.Changes, ch)
	}
	return nil
}

type field struct{ name, value string }

func optAmount(a *money.Amount) string {
	if a == nil {
		return "null"
	}
	return a.String()
}

func priceFields(p *pricing.Price) []field {
	sched := "null"
	if len(p.Schedule) > 0 {
		b, _ := json.Marshal(p.Schedule)
		sched = string(b) + " @ " + p.ScheduleTimezone
	}
	tiers := "null"
	if len(p.Tiers) > 0 {
		b, _ := json.Marshal(pricing.TierInputs(p.Tiers))
		tiers = string(b)
	}
	return []field{{"inputPerM", p.InputPerM.String()}, {"outputPerM", p.OutputPerM.String()},
		{"cacheReadPerM", p.CacheReadPM.String()}, {"cacheWritePerM", p.CacheWritePM.String()},
		{"perRequest", p.PerRequest.String()}, {"perImage", p.PerImage.String()},
		{"imageInputPerM", optAmount(p.ImageInputPM)}, {"audioInputPerM", optAmount(p.AudioInputPM)},
		{"audioOutputPerM", optAmount(p.AudioOutputPM)}, {"perMinute", p.PerMinute.String()},
		{"perMCharacters", p.PerMCharacters.String()}, {"schedule", sched}, {"tiers", tiers}}
}

// priceSummary lists the non-zero fields of a new price.
func priceSummary(p *pricing.Price) []string {
	out := []string{}
	for _, f := range priceFields(p) {
		if f.value != "0" && f.value != "null" {
			out = append(out, f.name+" "+f.value)
		}
	}
	if len(out) == 0 {
		out = append(out, "免费（全部为 0）")
	}
	return out
}

func priceDiff(a, b *pricing.Price) []string {
	fa, fb := priceFields(a), priceFields(b)
	out := []string{}
	for i := range fa {
		if fa[i].value != fb[i].value {
			out = append(out, fmt.Sprintf("%s %s → %s", fa[i].name, fa[i].value, fb[i].value))
		}
	}
	return out
}

// ---- model info ----

func (e ModelInfoEntry) input() plaza.Input {
	raw := func(v any) json.RawMessage {
		b, _ := json.Marshal(v)
		return b
	}
	return plaza.Input{DisplayName: raw(e.DisplayName), Description: raw(e.Description), Vendor: raw(e.Vendor),
		Tags: raw(e.Tags), ContextWindow: raw(e.ContextWindow), MaxOutput: raw(e.MaxOutput),
		Capabilities: raw(e.Capabilities), Hidden: raw(e.Hidden), SortOrder: raw(e.SortOrder)}
}

func (s Services) planModelInfo(ctx context.Context, entries []ModelInfoEntry, ps *problems, rep *Report) error {
	if len(entries) == 0 {
		return nil
	}
	all, err := s.Info.All(ctx)
	if err != nil {
		return err
	}
	seen := map[string]int{}
	for i, e := range entries {
		path := fmt.Sprintf("modelInfo[%d]", i)
		before := len(*ps)
		in := e.input()
		next, err := plaza.ValidateInput(e.Model, in)
		if err != nil {
			d := validationDetails(err)
			if d == nil {
				return err
			}
			ps.addDetails(path, d, nil)
		}
		if j, dup := seen[e.Model]; dup {
			ps.add(path+".model", "与 modelInfo[%d] 重复", j)
		}
		seen[e.Model] = i
		if len(*ps) > before {
			continue
		}
		cur := all[e.Model]
		ch := Change{Section: "model-info", Key: e.Model, infoModel: e.Model, info: &in}
		switch {
		case cur == nil:
			ch.Action = ActionCreate
			ch.Details = []string{infoSummary(next)}
		case plaza.SameContent(cur, next):
			ch.Action, ch.info = ActionUnchanged, nil
		default:
			v := cur.Version
			in.Version = &v
			ch.Action = ActionUpdate
			ch.Details = []string{strings.Join(infoDiff(cur, next), "，")}
		}
		rep.Changes = append(rep.Changes, ch)
	}
	return nil
}

func infoSummary(i *plaza.Info) string {
	parts := []string{}
	for _, p := range []string{i.DisplayName, i.Vendor} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if i.ContextWindow != nil {
		parts = append(parts, "contextWindow "+optInt(i.ContextWindow))
	}
	caps := []string{}
	for _, c := range []struct {
		on   bool
		name string
	}{{i.Capabilities.Vision, "vision"}, {i.Capabilities.Tools, "tools"}, {i.Capabilities.Reasoning, "reasoning"},
		{i.Capabilities.Embedding, "embedding"}, {i.Capabilities.ImageGeneration, "imageGeneration"},
		{i.Capabilities.AudioInput, "audioInput"}, {i.Capabilities.AudioOutput, "audioOutput"}, {i.Capabilities.Completions, "completions"}} {
		if c.on {
			caps = append(caps, c.name)
		}
	}
	if len(caps) > 0 {
		parts = append(parts, strings.Join(caps, "/"))
	}
	if i.Hidden {
		parts = append(parts, "hidden")
	}
	return strings.Join(parts, "，")
}

func short(s string) string {
	if utf8.RuneCountInString(s) <= 24 {
		return fmt.Sprintf("%q", s)
	}
	r := []rune(s)
	return fmt.Sprintf("%q…", string(r[:24]))
}

func optInt(v *int64) string {
	if v == nil {
		return "null"
	}
	return fmt.Sprint(*v)
}

func infoDiff(a, b *plaza.Info) []string {
	out := []string{}
	add := func(name, x, y string) {
		if x != y {
			out = append(out, fmt.Sprintf("%s %s → %s", name, x, y))
		}
	}
	add("displayName", short(a.DisplayName), short(b.DisplayName))
	if a.Description != b.Description {
		out = append(out, fmt.Sprintf("description 已修改（%d → %d 字）", utf8.RuneCountInString(a.Description), utf8.RuneCountInString(b.Description)))
	}
	add("vendor", short(a.Vendor), short(b.Vendor))
	add("tags", fmt.Sprint(a.Tags), fmt.Sprint(b.Tags))
	add("contextWindow", optInt(a.ContextWindow), optInt(b.ContextWindow))
	add("maxOutput", optInt(a.MaxOutput), optInt(b.MaxOutput))
	ca, _ := json.Marshal(a.Capabilities)
	cb, _ := json.Marshal(b.Capabilities)
	add("capabilities", string(ca), string(cb))
	add("hidden", fmt.Sprint(a.Hidden), fmt.Sprint(b.Hidden))
	add("sortOrder", fmt.Sprint(a.SortOrder), fmt.Sprint(b.SortOrder))
	return out
}

// ---- plans ----

func (s Services) planPlans(ctx context.Context, entries []PlanEntry, ps *problems, rep *Report) error {
	if len(entries) == 0 {
		return nil
	}
	existing, _, err := s.Plans.ListPlans(ctx, "", 0, 1_000_000)
	if err != nil {
		return err
	}
	byName := map[string][]*subscription.Plan{}
	for _, p := range existing {
		byName[p.Name] = append(byName[p.Name], p)
	}
	seen := map[string]int{}
	for i, e := range entries {
		path := fmt.Sprintf("plans[%d]", i)
		before := len(*ps)
		in := subscription.PlanInput{Name: e.Name, Description: e.Description, Duration: e.Duration, Models: e.Models,
			Rules: e.Rules, Stackable: e.Stackable, Status: e.Status}
		if e.ListPrice != nil {
			a, err := money.Parse(*e.ListPrice)
			if err != nil {
				ps.add(path+".listPrice", "不是合法的金额（十进制字符串，最多 9 位小数）或 null")
			} else {
				in.ListPrice = &a
			}
		}
		next, err := s.Plans.ValidatePlan(ctx, in)
		if err != nil {
			d := validationDetails(err)
			if d == nil {
				return err
			}
			ps.addDetails(path, d, nil)
		}
		if j, dup := seen[next.Name]; dup {
			ps.add(path+".name", "与 plans[%d] 同名（套餐按名称匹配）", j)
		}
		seen[next.Name] = i
		matches := byName[next.Name]
		if len(matches) > 1 {
			ps.add(path+".name", "数据库中有 %d 个名为 %q 的套餐，无法确定要更新哪一个（请先在后台改名）", len(matches), next.Name)
		}
		if len(*ps) > before {
			continue
		}
		ch := Change{Section: "plan", Key: next.Name, plan: &next}
		if len(matches) == 0 {
			ch.Action = ActionCreate
			ch.Details = []string{planSummary(&next)}
		} else {
			cur := matches[0]
			if diff := planDiff(cur, &next); len(diff) == 0 {
				ch.Action, ch.plan = ActionUnchanged, nil
			} else {
				ch.Action, ch.Details, ch.planID, ch.planVersion = ActionUpdate, diff, cur.ID, cur.Version
				if cur.Subscribers > 0 {
					ch.Details = append(ch.Details, fmt.Sprintf("（%d 个有效订阅保留开通时的快照，不受影响）", cur.Subscribers))
				}
			}
		}
		rep.Changes = append(rep.Changes, ch)
	}
	return nil
}

func listPriceText(p *money.Amount) string {
	if p == nil {
		return "未标价"
	}
	return p.String()
}

func modelsText(m []string) string {
	if len(m) == 0 {
		return "全部模型"
	}
	return strings.Join(m, ", ")
}

func windowText(w subscription.Window) string {
	switch w.Kind {
	case subscription.WindowCalendar:
		return fmt.Sprintf("calendar %s %s", w.Unit, w.Timezone)
	case subscription.WindowRolling, subscription.WindowSession:
		return w.Kind + " " + w.Duration
	case subscription.WindowPeriod:
		return "period " + w.Every
	}
	return w.Kind
}

func ruleText(r subscription.Rule) string {
	return fmt.Sprintf("%s（%s）：%s ≤ %s / %s", r.ID, r.DisplayName(), r.Meter, r.Limit, windowText(r.Window))
}

func planSummary(p *subscription.PlanInput) string {
	rules := make([]string, len(p.Rules))
	for i, r := range p.Rules {
		rules[i] = ruleText(r)
	}
	return fmt.Sprintf("listPrice %s，duration %s，%s，%s；规则：%s", listPriceText(p.ListPrice), p.Duration,
		modelsText(p.Models), map[bool]string{true: "可叠加", false: "不可叠加"}[p.Stackable], strings.Join(rules, "；"))
}

// comparable strips the read-only custom meter pins (re-resolved on save).
func comparableRule(r subscription.Rule) string {
	r.PluginVersionID, r.MeterLabel, r.MeterUnit = nil, "", ""
	b, _ := json.Marshal(r)
	return string(b)
}

func planDiff(cur *subscription.Plan, next *subscription.PlanInput) []string {
	out := []string{}
	add := func(name, x, y string) {
		if x != y {
			out = append(out, fmt.Sprintf("%s：%s → %s", name, x, y))
		}
	}
	if cur.Description != next.Description {
		out = append(out, fmt.Sprintf("description：已修改（%d → %d 字）", utf8.RuneCountInString(cur.Description),
			utf8.RuneCountInString(next.Description)))
	}
	add("listPrice", listPriceText(cur.ListPrice), listPriceText(next.ListPrice))
	add("duration", cur.Duration, next.Duration)
	add("models", modelsText(cur.Models), modelsText(next.Models))
	add("stackable", fmt.Sprint(cur.Stackable), fmt.Sprint(next.Stackable))
	add("status", cur.Status, next.Status)

	old := map[string]subscription.Rule{}
	oldIDs := make([]string, len(cur.Rules))
	for i, r := range cur.Rules {
		old[r.ID], oldIDs[i] = r, r.ID
	}
	newIDs := make([]string, len(next.Rules))
	for i, r := range next.Rules {
		newIDs[i] = r.ID
		prev, ok := old[r.ID]
		switch {
		case !ok:
			out = append(out, "新增规则 "+ruleText(r))
		case comparableRule(prev) != comparableRule(r):
			out = append(out, "修改规则 "+ruleText(prev)+" → "+ruleText(r))
		}
	}
	for _, r := range cur.Rules {
		if !slices.Contains(newIDs, r.ID) {
			out = append(out, "删除规则 "+ruleText(r))
		}
	}
	if len(out) == 0 && !slices.Equal(oldIDs, newIDs) {
		out = append(out, fmt.Sprintf("规则顺序：%v → %v", oldIDs, newIDs))
	}
	return out
}

// ---- output ----

var actionText = map[Action]string{ActionCreate: "新建", ActionNewVersion: "新版本", ActionUpdate: "更新", ActionUnchanged: "不变"}

var sectionText = map[string]string{"price": "价格", "model-info": "模型资料", "plan": "套餐"}

// Print writes the change table and the summary.
func (r *Report) Print(w io.Writer) {
	rows := [][]string{{"类别", "对象", "操作", "说明"}}
	for _, c := range r.Changes {
		act := actionText[c.Action]
		if r.DryRun && c.Action != ActionUnchanged {
			act += "（试运行）"
		} else if !r.DryRun && c.Action != ActionUnchanged && !c.Applied {
			act += "（未执行）"
		}
		first := ""
		if len(c.Details) > 0 {
			first = c.Details[0]
		}
		rows = append(rows, []string{sectionText[c.Section], c.Key, act, first})
		for _, d := range c.Details[min(1, len(c.Details)):] {
			rows = append(rows, []string{"", "", "", d})
		}
	}
	writeTable(w, rows)
	for _, warn := range r.Warnings {
		fmt.Fprintln(w, "警告：", warn)
	}
	fmt.Fprintln(w, "汇总：", r.Summary())
	switch {
	case r.DryRun:
		fmt.Fprintf(w, "试运行：未写入任何数据（%d 项将会变更）。\n", r.Pending())
	case r.Pending() == 0:
		fmt.Fprintln(w, "无需变更：数据库已与目录一致。")
	case r.Applied() < r.Pending():
		fmt.Fprintf(w, "只写入了 %d / %d 项变更（审计日志 requestId = %s）；修正错误后重新执行即可继续。\n", r.Applied(), r.Pending(), r.RequestID)
	default:
		fmt.Fprintf(w, "已写入 %d 项变更（审计日志 requestId = %s，metadata.source = seed）。\n", r.Applied(), r.RequestID)
	}
}

// writeTable aligns columns by display width (CJK characters take two
// terminal cells; text/tabwriter counts them as one).
func writeTable(w io.Writer, rows [][]string) {
	widths := map[int]int{}
	for _, row := range rows {
		for i, cell := range row[:len(row)-1] {
			widths[i] = max(widths[i], displayWidth(cell))
		}
	}
	for _, row := range rows {
		var b strings.Builder
		for i, cell := range row {
			b.WriteString(cell)
			if i < len(row)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-displayWidth(cell)+2))
			}
		}
		fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
	}
}

func displayWidth(s string) int {
	n := 0
	for _, r := range s {
		switch {
		case r >= 0x1100 && (r <= 0x115f || (r >= 0x2e80 && r <= 0xa4cf) || (r >= 0xac00 && r <= 0xd7a3) ||
			(r >= 0xf900 && r <= 0xfaff) || (r >= 0xfe30 && r <= 0xfe4f) || (r >= 0xff00 && r <= 0xff60) ||
			(r >= 0xffe0 && r <= 0xffe6)):
			n += 2
		default:
			n++
		}
	}
	return n
}
