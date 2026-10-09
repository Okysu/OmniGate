// Package pricing stores versioned prices and computes request cost and charge
// (ADR-0006, ADR-0007). Prices are immutable: changing a price adds a version.
package pricing

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/apperr"
	"omnigate/internal/audit"
	"omnigate/internal/authz"
	"omnigate/internal/money"
	"omnigate/internal/platform/db"
	"omnigate/internal/platform/tzcache"
	"omnigate/internal/protocol"
)

const (
	KindSell = "sell"
	KindCost = "cost"
)

type Price struct {
	ID           uuid.UUID    `json:"id"`
	Kind         string       `json:"kind"`
	Model        string       `json:"model"`
	ChannelID    *uuid.UUID   `json:"channelId"`
	InputPerM    money.Amount `json:"-"`
	OutputPerM   money.Amount `json:"-"`
	CacheReadPM  money.Amount `json:"-"`
	CacheWritePM money.Amount `json:"-"`
	PerRequest   money.Amount `json:"-"`
	// PerImage is charged per output image (0 = not charged); ImageInputPM
	// prices image input tokens (nil = billed at InputPerM). phase7-api.md §1.1.
	PerImage     money.Amount  `json:"-"`
	ImageInputPM *money.Amount `json:"-"`
	// Audio (phase9-api.md §1.1): AudioInputPM / AudioOutputPM price audio
	// tokens (nil = billed at InputPerM / OutputPerM); PerMinute is charged per
	// minute of input audio, prorated per second; PerMCharacters per million
	// speech input characters (0 = not charged).
	AudioInputPM   *money.Amount `json:"-"`
	AudioOutputPM  *money.Amount `json:"-"`
	PerMinute      money.Amount  `json:"-"`
	PerMCharacters money.Amount  `json:"-"`
	// Schedule is the time-of-day multiplier schedule (nil = none) evaluated
	// in ScheduleTimezone (phase8-api.md §3).
	Schedule         []ScheduleSlot `json:"schedule"`
	ScheduleTimezone string         `json:"scheduleTimezone"`
	// Tiers are the context-length tiers, ascending by threshold (nil = none;
	// phase10-api.md §1).
	Tiers       []Tier     `json:"-"`
	EffectiveAt time.Time  `json:"effectiveAt"`
	CreatedAt   time.Time  `json:"createdAt"`
	CreatedBy   *uuid.UUID `json:"createdBy"`
}

// priceJSON renders amounts as decimal strings.
type priceJSON struct {
	Price
	InputPerM       string  `json:"inputPerM"`
	OutputPerM      string  `json:"outputPerM"`
	CacheReadPerM   string  `json:"cacheReadPerM"`
	CacheWritePerM  string  `json:"cacheWritePerM"`
	PerRequest      string  `json:"perRequest"`
	PerImage        string  `json:"perImage"`
	ImageInputPerM  *string `json:"imageInputPerM"`
	AudioInputPerM  *string `json:"audioInputPerM"`
	AudioOutputPerM *string `json:"audioOutputPerM"`
	PerMinute       string  `json:"perMinute"`
	PerMCharacters  string  `json:"perMCharacters"`
	// Tiers: null = none; tier fields null = inherited from the base prices.
	Tiers []TierInput `json:"tiers"`
}

func optionalString(a *money.Amount) *string {
	if a == nil {
		return nil
	}
	v := a.String()
	return &v
}

func (p *Price) JSON() any {
	if p == nil {
		return nil
	}
	out := priceJSON{Price: *p, InputPerM: p.InputPerM.String(), OutputPerM: p.OutputPerM.String(),
		CacheReadPerM: p.CacheReadPM.String(), CacheWritePerM: p.CacheWritePM.String(), PerRequest: p.PerRequest.String(),
		PerImage: p.PerImage.String(), ImageInputPerM: optionalString(p.ImageInputPM),
		AudioInputPerM: optionalString(p.AudioInputPM), AudioOutputPerM: optionalString(p.AudioOutputPM),
		PerMinute: p.PerMinute.String(), PerMCharacters: p.PerMCharacters.String(), Tiers: TierInputs(p.Tiers)}
	return out
}

// AudioInputPrice is the price per million audio input tokens (InputPerM
// unless set).
func (p *Price) AudioInputPrice() money.Amount {
	if p.AudioInputPM != nil {
		return *p.AudioInputPM
	}
	return p.InputPerM
}

// AudioOutputPrice is the price per million audio output tokens (OutputPerM
// unless set).
func (p *Price) AudioOutputPrice() money.Amount {
	if p.AudioOutputPM != nil {
		return *p.AudioOutputPM
	}
	return p.OutputPerM
}

// ImageInputPrice is the price per million image input tokens (InputPerM
// unless set).
func (p *Price) ImageInputPrice() money.Amount {
	if p.ImageInputPM != nil {
		return *p.ImageInputPM
	}
	return p.InputPerM
}

// Compute prices usage of a request that started at `at`. A nil price costs
// zero.
//
// base = perRequest + text input × inputPerM + image input × imageInputPerM
// + audio input × audioInputPerM + text output × outputPerM + audio output ×
// audioOutputPerM + cache reads/writes + images × perImage + audio seconds ×
// perMinute / 60 + characters × perMCharacters / 1M, where text input =
// Input − ImageInput − AudioInput and text output = Output − AudioOutput
// (phase7-api.md §1.1, §4.8; phase9-api.md §1.1), with the unit prices of the
// context-length tier selected by the request's prompt tokens (input + cache
// read + cache write, phase10-api.md §1) when the version has tiers;
// amount = base × the version's schedule multiplier at `at` × group
// (phase8-api.md §1.1, §3), rounded once. group is the user group's price
// multiplier for sell prices and One for cost prices: a group never changes
// what the platform pays upstream.
//
// Compute never wraps around: negative counts are treated as 0 and every
// multiplication and sum is checked, so an amount that does not fit
// money.Amount returns money.ErrOverflow (the gateway rejects such a request
// before it is admitted; see protocol.MaxTokensLimit).
func (p *Price) Compute(u protocol.Usage, at time.Time, group Multiplier) (money.Amount, error) {
	if p == nil {
		return 0, nil
	}
	base, err := p.WithTier(p.TierFor(PromptTokens(u))).base(u)
	if err != nil {
		return 0, err
	}
	return applyFactors(base, p.ScheduleMultiplier(at), group)
}

// base is the amount before multipliers.
func (p *Price) base(u protocol.Usage) (money.Amount, error) {
	input, output := max(u.Input, 0), max(u.Output, 0)
	imageIn := min(max(u.ImageInput, 0), input)
	audioIn := min(max(u.AudioInput, 0), input-imageIn)
	audioOut := min(max(u.AudioOutput, 0), output)
	perImages, err := p.PerImage.MulDiv(max(u.Images, 0), 1)
	if err != nil {
		return 0, err
	}
	total, err := p.PerRequest.Add(perImages)
	if err != nil {
		return 0, err
	}
	perSeconds, err := p.PerMinute.MulDiv(max(u.AudioSeconds, 0), 60)
	if err != nil {
		return 0, err
	}
	if total, err = total.Add(perSeconds); err != nil {
		return 0, err
	}
	for _, part := range []struct {
		price  money.Amount
		tokens int64
	}{{p.InputPerM, input - imageIn - audioIn}, {p.ImageInputPrice(), imageIn}, {p.AudioInputPrice(), audioIn},
		{p.OutputPerM, output - audioOut}, {p.AudioOutputPrice(), audioOut}, {p.CacheReadPM, max(u.CacheRead, 0)},
		{p.CacheWritePM, max(u.CacheWrite, 0)}, {p.PerMCharacters, max(u.Characters, 0)}} {
		c, err := money.TokenCost(part.price, part.tokens)
		if err != nil {
			return 0, err
		}
		if total, err = total.Add(c); err != nil {
			return 0, err
		}
	}
	return total, nil
}

type Service struct {
	pool  *db.DB
	audit *audit.Recorder

	mu    sync.RWMutex
	cache map[string][]*Price // key -> versions sorted by effective_at desc
	at    time.Time

	// OnSellPrice, when set, is called after a sell price version is created
	// with the version it replaces at its effective time (nil when none).
	OnSellPrice func(ctx context.Context, prev, next *Price)
}

// SamePrice reports whether two versions charge the same amounts.
func SamePrice(a, b *Price) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.InputPerM == b.InputPerM && a.OutputPerM == b.OutputPerM && a.CacheReadPM == b.CacheReadPM &&
		a.CacheWritePM == b.CacheWritePM && a.PerRequest == b.PerRequest && a.PerImage == b.PerImage &&
		sameOptional(a.ImageInputPM, b.ImageInputPM) && sameOptional(a.AudioInputPM, b.AudioInputPM) &&
		sameOptional(a.AudioOutputPM, b.AudioOutputPM) && a.PerMinute == b.PerMinute && a.PerMCharacters == b.PerMCharacters &&
		sameSchedule(a, b) && sameTiers(a.Tiers, b.Tiers)
}

func sameSchedule(a, b *Price) bool {
	if len(a.Schedule) == 0 && len(b.Schedule) == 0 {
		return true
	}
	x, _ := json.Marshal(a.Schedule)
	y, _ := json.Marshal(b.Schedule)
	return string(x) == string(y) && a.ScheduleTimezone == b.ScheduleTimezone
}

func sameOptional(a, b *money.Amount) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func NewService(pool *db.DB, rec *audit.Recorder) *Service {
	return &Service{pool: pool, audit: rec}
}

const priceCols = `id, kind, model, channel_id, input_per_m, output_per_m, cache_read_per_m, cache_write_per_m, per_request, effective_at, created_at, created_by, per_image, image_input_per_m, schedule, schedule_timezone, audio_input_per_m, audio_output_per_m, per_minute, per_m_characters, tiers`

func scanPrice(row db.Row) (*Price, error) {
	var p Price
	var imageIn, audioIn, audioOut *int64
	var sched, tiers []byte
	err := row.Scan(&p.ID, &p.Kind, &p.Model, &p.ChannelID, &p.InputPerM, &p.OutputPerM, &p.CacheReadPM, &p.CacheWritePM,
		&p.PerRequest, &p.EffectiveAt, &p.CreatedAt, &p.CreatedBy, &p.PerImage, &imageIn, &sched, &p.ScheduleTimezone,
		&audioIn, &audioOut, &p.PerMinute, &p.PerMCharacters, &tiers)
	p.ImageInputPM, p.AudioInputPM, p.AudioOutputPM = optionalAmount(imageIn), optionalAmount(audioIn), optionalAmount(audioOut)
	p.Schedule = decodeSchedule(sched)
	p.Tiers = decodeTiers(tiers)
	return &p, err
}

func optionalAmount(v *int64) *money.Amount {
	if v == nil {
		return nil
	}
	a := money.Amount(*v)
	return &a
}

func nullableNano(a *money.Amount) *int64 {
	if a == nil {
		return nil
	}
	v := int64(*a)
	return &v
}

func cacheKey(kind, model string, ch *uuid.UUID) string {
	k := kind + "|" + model
	if ch != nil {
		k += "|" + ch.String()
	}
	return k
}

// reload refreshes the in-memory price table at most every 10 seconds.
func (s *Service) reload(ctx context.Context, force bool) error {
	s.mu.RLock()
	fresh := !force && time.Since(s.at) < 10*time.Second && s.cache != nil
	s.mu.RUnlock()
	if fresh {
		return nil
	}
	rows, err := s.pool.Query(ctx, `SELECT `+priceCols+` FROM prices ORDER BY effective_at DESC, created_at DESC`)
	if err != nil {
		return err
	}
	defer rows.Close()
	cache := map[string][]*Price{}
	for rows.Next() {
		p, err := scanPrice(rows)
		if err != nil {
			return err
		}
		k := cacheKey(p.Kind, p.Model, p.ChannelID)
		cache[k] = append(cache[k], p)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.cache, s.at = cache, time.Now()
	s.mu.Unlock()
	return nil
}

// Lookup returns the price version effective at `at` (nil when none).
func (s *Service) Lookup(ctx context.Context, kind, model string, channelID *uuid.UUID, at time.Time) (*Price, error) {
	if err := s.reload(ctx, false); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.cache[cacheKey(kind, model, channelID)] {
		if !p.EffectiveAt.After(at) {
			return p, nil
		}
	}
	return nil, nil
}

// CreateInput is the body of POST /api/admin/prices.
type CreateInput struct {
	Kind           string     `json:"kind"`
	Model          string     `json:"model"`
	ChannelID      *uuid.UUID `json:"channelId"`
	InputPerM      string     `json:"inputPerM"`
	OutputPerM     string     `json:"outputPerM"`
	CacheReadPerM  string     `json:"cacheReadPerM"`
	CacheWritePerM string     `json:"cacheWritePerM"`
	PerRequest     string     `json:"perRequest"`
	// PerImage: omitted / null / "" = 0. ImageInputPerM: omitted / null / "" =
	// billed at inputPerM (phase7-api.md §4.6).
	PerImage       *string `json:"perImage"`
	ImageInputPerM *string `json:"imageInputPerM"`
	// Audio (phase9-api.md §1.1): AudioInputPerM / AudioOutputPerM: omitted /
	// null / "" = billed at inputPerM / outputPerM; PerMinute, PerMCharacters:
	// omitted / null / "" = 0.
	AudioInputPerM  *string    `json:"audioInputPerM"`
	AudioOutputPerM *string    `json:"audioOutputPerM"`
	PerMinute       *string    `json:"perMinute"`
	PerMCharacters  *string    `json:"perMCharacters"`
	EffectiveAt     *time.Time `json:"effectiveAt"`
	// Schedule: omitted / null / [] = none; ScheduleTimezone: omitted / "" =
	// Asia/Shanghai (phase8-api.md §3).
	Schedule         []ScheduleSlot `json:"schedule"`
	ScheduleTimezone *string        `json:"scheduleTimezone"`
	// Tiers: omitted / null / [] = none (phase10-api.md §1).
	Tiers []TierInput `json:"tiers"`
}

func parseAmount(s, field string, details map[string]any) money.Amount {
	if s == "" {
		return 0
	}
	a, err := money.Parse(s)
	if err != nil || a < 0 {
		details[field] = "必须是非负十进制数，最多 9 位小数"
	}
	return a
}

// Prepare validates in exactly like Create and returns the version Create
// would store (without ID and CreatedBy), so callers can compare it with the
// current version or report it without writing (`omnigate seed --dry-run`).
func Prepare(in CreateInput) (*Price, error) {
	return prepare(in, time.Now().UTC())
}

func prepare(in CreateInput, now time.Time) (*Price, error) {
	details := map[string]any{}
	if in.Kind != KindSell && in.Kind != KindCost {
		details["kind"] = "只能是 sell 或 cost"
	}
	if in.Model == "" || len(in.Model) > 128 {
		details["model"] = "长度应为 1–128 个字符"
	}
	if (in.Kind == KindCost) != (in.ChannelID != nil) {
		details["channelId"] = "cost 价格必须指定渠道，sell 价格不能指定渠道"
	}
	eff := now
	if in.EffectiveAt != nil {
		if in.EffectiveAt.Before(now.Add(-time.Minute)) {
			details["effectiveAt"] = "不能早于当前时间（历史账单不可追溯修改）"
		}
		eff = in.EffectiveAt.UTC()
	}
	pr := &Price{Kind: in.Kind, Model: in.Model, ChannelID: in.ChannelID, EffectiveAt: eff, CreatedAt: now,
		InputPerM:    parseAmount(in.InputPerM, "inputPerM", details),
		OutputPerM:   parseAmount(in.OutputPerM, "outputPerM", details),
		CacheReadPM:  parseAmount(in.CacheReadPerM, "cacheReadPerM", details),
		CacheWritePM: parseAmount(in.CacheWritePerM, "cacheWritePerM", details),
		PerRequest:   parseAmount(in.PerRequest, "perRequest", details),
	}
	if in.PerImage != nil {
		pr.PerImage = parseAmount(*in.PerImage, "perImage", details)
	}
	optional := func(s *string, field string) *money.Amount {
		if s == nil || *s == "" {
			return nil
		}
		a := parseAmount(*s, field, details)
		return &a
	}
	pr.ImageInputPM = optional(in.ImageInputPerM, "imageInputPerM")
	pr.AudioInputPM = optional(in.AudioInputPerM, "audioInputPerM")
	pr.AudioOutputPM = optional(in.AudioOutputPerM, "audioOutputPerM")
	if in.PerMinute != nil {
		pr.PerMinute = parseAmount(*in.PerMinute, "perMinute", details)
	}
	if in.PerMCharacters != nil {
		pr.PerMCharacters = parseAmount(*in.PerMCharacters, "perMCharacters", details)
	}
	pr.ScheduleTimezone = tzcache.Default
	if in.ScheduleTimezone != nil && strings.TrimSpace(*in.ScheduleTimezone) != "" {
		pr.ScheduleTimezone = strings.TrimSpace(*in.ScheduleTimezone)
		if !tzcache.Valid(pr.ScheduleTimezone) {
			details["scheduleTimezone"] = "无效的时区（IANA 名称，如 Asia/Shanghai）"
		}
	}
	if sched, err := compileSchedule(in.Schedule); err != nil {
		details["schedule"] = err.Error()
	} else {
		pr.Schedule = sched
	}
	pr.Tiers = compileTiers(in.Tiers, details)
	if len(details) > 0 {
		return nil, apperr.Validation("价格参数校验失败", details)
	}
	return pr, nil
}

func (s *Service) Create(ctx context.Context, p *authz.Principal, in CreateInput, ipPrefix, requestID string) (*Price, error) {
	pr, err := prepare(in, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	pr.ID = id
	// The nil user is the system actor (`omnigate seed`): no creator row.
	if p.UserID != uuid.Nil {
		pr.CreatedBy = &p.UserID
	}
	var prev *Price
	if pr.Kind == KindSell && s.OnSellPrice != nil {
		if prev, err = s.Lookup(ctx, KindSell, pr.Model, nil, pr.EffectiveAt); err != nil {
			return nil, err
		}
	}
	err = db.InTx(ctx, s.pool, func(tx db.Tx) error {
		var sched any
		if len(pr.Schedule) > 0 {
			b, _ := json.Marshal(pr.Schedule)
			sched = b
		}
		if _, err := tx.Exec(ctx, `INSERT INTO prices (`+priceCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)`,
			pr.ID, pr.Kind, pr.Model, pr.ChannelID, pr.InputPerM, pr.OutputPerM, pr.CacheReadPM, pr.CacheWritePM, pr.PerRequest,
			pr.EffectiveAt, pr.CreatedAt, pr.CreatedBy, pr.PerImage, nullableNano(pr.ImageInputPM), sched, pr.ScheduleTimezone,
			nullableNano(pr.AudioInputPM), nullableNano(pr.AudioOutputPM), pr.PerMinute, pr.PerMCharacters, encodeTiers(pr.Tiers)); err != nil {
			if db.IsForeignKeyViolation(err) {
				return apperr.Validation("价格参数校验失败", map[string]any{"channelId": "渠道不存在"})
			}
			return err
		}
		rid := pr.ID.String()
		return s.audit.Record(ctx, tx, audit.Entry{ActorID: &p.UserID, ActorName: &p.Name, Action: "price.create", ResourceType: "price",
			ResourceID: &rid, IPPrefix: ipPrefix, RequestID: requestID,
			Metadata: map[string]any{"kind": pr.Kind, "model": pr.Model, "inputPerM": pr.InputPerM.String(), "outputPerM": pr.OutputPerM.String(),
				"perImage": pr.PerImage.String(), "perMinute": pr.PerMinute.String(), "perMCharacters": pr.PerMCharacters.String(), "effectiveAt": pr.EffectiveAt, "schedule": pr.Schedule, "scheduleTimezone": pr.ScheduleTimezone,
				"tiers": TierInputs(pr.Tiers)}})
	})
	if err != nil {
		return nil, err
	}
	_ = s.reload(ctx, true)
	if pr.Kind == KindSell && s.OnSellPrice != nil {
		s.OnSellPrice(ctx, prev, pr)
	}
	return pr, nil
}

// Effective returns the version of every (kind, model, channel) effective at
// `at`, ordered by kind, model and channel (keys whose versions all start
// later are left out).
func (s *Service) Effective(ctx context.Context, at time.Time) ([]*Price, error) {
	if err := s.reload(ctx, true); err != nil {
		return nil, err
	}
	s.mu.RLock()
	out := []*Price{}
	for _, versions := range s.cache {
		for _, p := range versions {
			if !p.EffectiveAt.After(at) {
				out = append(out, p)
				break
			}
		}
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Kind != b.Kind {
			return a.Kind > b.Kind // sell before cost
		}
		if a.Model != b.Model {
			return a.Model < b.Model
		}
		return channelKey(a.ChannelID) < channelKey(b.ChannelID)
	})
	return out, nil
}

// Upcoming returns the versions of (kind, model, channel) that take effect
// after `at`, earliest first.
func (s *Service) Upcoming(ctx context.Context, kind, model string, channelID *uuid.UUID, at time.Time) ([]*Price, error) {
	if err := s.reload(ctx, false); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*Price
	for _, p := range s.cache[cacheKey(kind, model, channelID)] {
		if p.EffectiveAt.After(at) {
			out = append([]*Price{p}, out...)
		}
	}
	return out, nil
}

func channelKey(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}

type ListQuery struct {
	Kind, Model string
	ChannelID   *uuid.UUID
	Offset      int
	Limit       int
}

func (s *Service) List(ctx context.Context, q ListQuery) ([]any, int, error) {
	where, args := "WHERE true", []any{}
	add := func(cond string, v any) {
		args = append(args, v)
		where += " AND " + cond + " = $" + strconv.Itoa(len(args))
	}
	if q.Kind != "" {
		add("kind", q.Kind)
	}
	if q.Model != "" {
		add("model", q.Model)
	}
	if q.ChannelID != nil {
		add("channel_id", *q.ChannelID)
	}
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM prices `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, q.Limit, q.Offset)
	rows, err := s.pool.Query(ctx, `SELECT `+priceCols+` FROM prices `+where+` ORDER BY effective_at DESC, created_at DESC LIMIT $`+
		strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []any{}
	for rows.Next() {
		p, err := scanPrice(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, p.JSON())
	}
	return out, total, rows.Err()
}
