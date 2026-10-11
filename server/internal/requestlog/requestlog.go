// Package requestlog records per-request metadata asynchronously and serves
// log queries and aggregate statistics. Prompt and response bodies are never
// recorded.
package requestlog

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"omnigate/internal/journal"
	"omnigate/internal/money"
	"omnigate/internal/platform/db"
	"omnigate/internal/protocol"
)

// Attempt is one upstream try within a request.
type Attempt struct {
	ChannelID   uuid.UUID `json:"channelId"`
	ChannelName string    `json:"channelName"`
	StatusCode  int       `json:"statusCode"`
	ErrorClass  *string   `json:"errorClass"`
	DurationMs  int64     `json:"durationMs"`
}

// Entry is one gateway request.
type Entry struct {
	ID        uuid.UUID
	StartedAt time.Time
	RequestID string
	UserID    *uuid.UUID
	KeyID     *uuid.UUID
	KeyName   *string
	Inbound   string
	Model     string
	// ServedModel is the logical model that served the request ("" = Model;
	// differs after a fallback to another model).
	ServedModel   string
	ChannelID     *uuid.UUID
	ChannelName   *string
	UpstreamModel *string
	Stream        bool
	StatusCode    int
	ErrorClass    *string
	ErrorMessage  *string
	Attempts      []Attempt
	TTFTMs        *int64
	DurationMs    int64
	Usage         protocol.Usage
	Cost          money.Amount
	Charge        money.Amount
	SellPriceID   *uuid.UUID
	CostPriceID   *uuid.UUID
	IPPrefix      string
	// SubscriptionID is set when a plan covered the request (Charge is then 0
	// and QuotaCharge holds the sell-price value counted against the plan).
	SubscriptionID *uuid.UUID
	QuotaCharge    money.Amount
	// ChannelTier is the tier (own | shared | platform) of the channel that
	// served the request, or of the last attempted channel ("" = none).
	ChannelTier string
	// PriceMultiplier is the effective sell-price multiplier (group ×
	// schedule, decimal text) of a priced platform request; nil otherwise.
	PriceMultiplier *string
	// PriceTier is the aboveInputTokens of the sell-price context-length tier
	// that priced the request (phase10-api.md §1); nil = base prices.
	PriceTier *int64
	// Affinity is the session affinity outcome (affinity.Outcome*) and
	// AffinityRule the applying rule's name (phase12-api.md §4); nil when no
	// rule applied. The session value itself is never recorded.
	Affinity     *string
	AffinityRule *string
	// Client is the detected client id (clientdetect.Known) and
	// ClientVersion its version (nil = not parseable; phase13-api.md §2).
	// The raw User-Agent and headers are never recorded.
	Client        *string
	ClientVersion *string
}

var (
	metricRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "omnigate_gateway_requests_total", Help: "Gateway requests by inbound protocol and outcome.",
	}, []string{"inbound", "outcome"})
	metricLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name: "omnigate_gateway_request_duration_seconds", Help: "Gateway request duration.",
		Buckets: []float64{.05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60, 120, 300},
	}, []string{"inbound"})
	metricDropped = promauto.NewCounter(prometheus.CounterOpts{
		Name: "omnigate_request_log_dropped_total", Help: "Request log entries dropped because the buffer was full.",
	})
)

// JournalKind is the settlement journal kind of a request log batch.
const JournalKind = "request_logs"

// Writer buffers entries and inserts them in batches (COPY on PostgreSQL, one
// transaction with a prepared INSERT on SQLite). A failed batch is retried
// with backoff and, when the database stays unavailable, spilled to the
// settlement journal and replayed later (ADR-0010).
type Writer struct {
	pool    *db.DB
	log     *slog.Logger
	ch      chan *Entry
	flushRq chan chan error
	running atomic.Bool
	wg      sync.WaitGroup

	// Journal receives batches that could not be inserted (nil: they are
	// logged as errors and lost). Set before Run.
	Journal *journal.Journal
	// BeforeInsert, when set, runs before every insert; an error fails it
	// (tests: simulated database failures).
	BeforeInsert func(ctx context.Context) error
	// retryDelay is the first backoff between insert attempts (default 500ms).
	retryDelay time.Duration
	// FlushInterval is how often queued entries are written (default 1s). Set
	// before Run.
	FlushInterval time.Duration
}

func NewWriter(pool *db.DB, log *slog.Logger) *Writer {
	return &Writer{pool: pool, log: log, ch: make(chan *Entry, 10000), flushRq: make(chan chan error)}
}

// Add queues an entry. It waits up to 2s when the buffer is full (back-pressure
// rather than silent loss) and then drops, counting the drop in a metric.
func (w *Writer) Add(e *Entry) {
	outcome := "success"
	if e.StatusCode >= 400 {
		outcome = "error"
	}
	metricRequests.WithLabelValues(e.Inbound, outcome).Inc()
	metricLatency.WithLabelValues(e.Inbound).Observe(float64(e.DurationMs) / 1000)
	select {
	case w.ch <- e:
		return
	default:
	}
	t := time.NewTimer(2 * time.Second)
	defer t.Stop()
	select {
	case w.ch <- e:
	case <-t.C:
		metricDropped.Inc()
		w.log.Error("request log buffer full; entry dropped", "request_id", e.RequestID)
	}
}

// Run flushes batches until ctx is done, then drains the buffer.
func (w *Writer) Run(ctx context.Context) {
	w.wg.Add(1)
	defer w.wg.Done()
	w.running.Store(true)
	defer w.running.Store(false)
	interval := w.FlushInterval
	if interval <= 0 {
		interval = time.Second
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	var batch []*Entry
	flush := func() {
		if len(batch) == 0 {
			return
		}
		_ = w.persist(ctx, batch)
		batch = batch[:0]
	}
	for {
		select {
		case e := <-w.ch:
			batch = append(batch, e)
			if len(batch) >= 500 {
				flush()
			}
		case <-tick.C:
			flush()
		case reply := <-w.flushRq:
			for drained := false; !drained; {
				select {
				case e := <-w.ch:
					batch = append(batch, e)
				default:
					drained = true
				}
			}
			err := w.persist(ctx, batch)
			batch = batch[:0]
			reply <- err
		case <-ctx.Done():
			for {
				select {
				case e := <-w.ch:
					batch = append(batch, e)
				default:
					flush()
					return
				}
			}
		}
	}
}

// Wait blocks until Run has drained after its context was cancelled.
func (w *Writer) Wait() { w.wg.Wait() }

// Flush synchronously writes everything queued so far, including a batch the
// running writer is holding (tests and diagnostics).
func (w *Writer) Flush(ctx context.Context) error {
	if w.running.Load() {
		reply := make(chan error, 1)
		select {
		case w.flushRq <- reply:
			return <-reply
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	var batch []*Entry
	for {
		select {
		case e := <-w.ch:
			batch = append(batch, e)
		default:
			return w.persist(ctx, batch)
		}
	}
}

// persist writes batch: up to three attempts with backoff (one while the
// journal already holds items, i.e. the database is known to be down), then
// the batch is spilled to the journal. It returns an error only when the
// batch could be neither inserted nor journaled (it is then logged in full).
func (w *Writer) persist(ctx context.Context, batch []*Entry) error {
	if len(batch) == 0 {
		return nil
	}
	attempts := 3
	if w.Journal != nil && w.Journal.Pending() > 0 {
		attempts = 1
	}
	delay := w.retryDelay
	if delay == 0 {
		delay = 500 * time.Millisecond
	}
	var err error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			time.Sleep(delay)
			delay *= 4
		}
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		if i == 0 {
			err = w.insert(fctx, batch)
		} else {
			// The previous attempt may have committed before failing:
			// retries skip rows that already exist.
			err = w.insertIdempotent(fctx, batch)
		}
		cancel()
		if err == nil {
			return nil
		}
		w.log.Warn("request log insert failed", "count", len(batch), "attempt", i+1, "err", err)
	}
	journal.Failed(JournalKind)
	if w.Journal != nil {
		jerr := w.Journal.Append(JournalKind, batch)
		if jerr == nil {
			w.log.Error("request log insert failed; batch saved to the settlement journal for replay",
				"count", len(batch), "err", err)
			return nil
		}
		err = fmt.Errorf("%w; journal: %w", err, jerr)
	}
	raw, _ := json.Marshal(batch)
	w.log.Error("request log batch lost: database and journal unavailable", "count", len(batch), "err", err, "entries", string(raw))
	return err
}

// Replay inserts a journaled batch (settlement journal handler); rows that
// already exist are skipped, so a batch can be replayed any number of times.
func (w *Writer) Replay(ctx context.Context, data json.RawMessage) error {
	var batch []*Entry
	if err := json.Unmarshal(data, &batch); err != nil {
		return fmt.Errorf("decode journaled request logs: %w", err)
	}
	return w.insertIdempotent(ctx, batch)
}

var columns = []string{"id", "started_at", "request_id", "user_id", "key_id", "key_name", "inbound", "model", "channel_id",
	"channel_name", "upstream_model", "stream", "status_code", "error_class", "error_message", "attempts", "fallback_path",
	"ttft_ms", "duration_ms", "input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "reasoning_tokens",
	"usage_estimated", "cost_nano", "charge_nano", "sell_price_id", "cost_price_id", "ip_prefix", "subscription_id", "quota_charge_nano", "served_model", "channel_tier",
	"image_count", "image_input_tokens", "price_multiplier", "audio_seconds", "audio_input_tokens", "audio_output_tokens", "input_characters", "price_tier",
	"affinity", "affinity_rule", "client", "client_version"}

func row(e *Entry) []any {
	path, _ := json.Marshal(e.Attempts)
	if e.Attempts == nil {
		path = []byte("[]")
	}
	return []any{e.ID, e.StartedAt, e.RequestID, e.UserID, e.KeyID, e.KeyName, e.Inbound, e.Model, e.ChannelID,
		e.ChannelName, e.UpstreamModel, e.Stream, e.StatusCode, e.ErrorClass, e.ErrorMessage, len(e.Attempts), string(path),
		e.TTFTMs, e.DurationMs, e.Usage.Input, e.Usage.Output, e.Usage.CacheRead, e.Usage.CacheWrite, e.Usage.Reasoning,
		e.Usage.Estimated, int64(e.Cost), int64(e.Charge), e.SellPriceID, e.CostPriceID, e.IPPrefix, e.SubscriptionID, int64(e.QuotaCharge),
		servedModel(e), channelTier(e), e.Usage.Images, e.Usage.ImageInput, e.PriceMultiplier, e.Usage.AudioSeconds, e.Usage.AudioInput,
		e.Usage.AudioOutput, e.Usage.Characters, e.PriceTier, e.Affinity, e.AffinityRule, e.Client, e.ClientVersion}
}

func (w *Writer) insert(ctx context.Context, batch []*Entry) error {
	if len(batch) == 0 {
		return nil
	}
	if w.BeforeInsert != nil {
		if err := w.BeforeInsert(ctx); err != nil {
			return err
		}
	}
	rows := make([][]any, len(batch))
	for i, e := range batch {
		rows[i] = row(e)
	}
	_, err := w.pool.CopyFrom(ctx, "request_logs", columns, rows)
	return err
}

// insertSQL inserts one row unless (id, started_at) — the primary key, which
// includes the partition key on PostgreSQL — already exists.
var insertSQL = func() string {
	var cols, vals strings.Builder
	for i, c := range columns {
		if i > 0 {
			cols.WriteString(", ")
			vals.WriteString(", ")
		}
		cols.WriteString(c)
		vals.WriteString("$" + strconv.Itoa(i+1))
	}
	return "INSERT INTO request_logs (" + cols.String() + ") VALUES (" + vals.String() + ") ON CONFLICT (id, started_at) DO NOTHING"
}()

// insertIdempotent inserts batch in one transaction, skipping rows that exist.
func (w *Writer) insertIdempotent(ctx context.Context, batch []*Entry) error {
	if len(batch) == 0 {
		return nil
	}
	if w.BeforeInsert != nil {
		if err := w.BeforeInsert(ctx); err != nil {
			return err
		}
	}
	return db.InTx(ctx, w.pool, func(tx db.Tx) error {
		for _, e := range batch {
			if _, err := tx.Exec(ctx, insertSQL, row(e)...); err != nil {
				return err
			}
		}
		return nil
	})
}

func servedModel(e *Entry) *string {
	if e.ServedModel == "" {
		return nil
	}
	return &e.ServedModel
}

func channelTier(e *Entry) *string {
	if e.ChannelTier == "" {
		return nil
	}
	return &e.ChannelTier
}

// EnsurePartitions creates monthly partitions for the current and next month
// (PostgreSQL; SQLite keeps request logs in one table and does nothing).
func EnsurePartitions(ctx context.Context, pool *db.DB, now time.Time) error {
	if pool.Dialect() != db.Postgres {
		return nil
	}
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		from := start.AddDate(0, i, 0)
		to := from.AddDate(0, 1, 0)
		name := fmt.Sprintf("request_logs_y%04dm%02d", from.Year(), int(from.Month()))
		_, err := pool.Exec(ctx, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s PARTITION OF request_logs FOR VALUES FROM ('%s') TO ('%s')`,
			name, from.Format(time.RFC3339), to.Format(time.RFC3339)))
		if err != nil {
			return fmt.Errorf("create partition %s: %w", name, err)
		}
	}
	return nil
}

// RunPartitionMaintenance ensures partitions daily (no-op on SQLite).
func RunPartitionMaintenance(ctx context.Context, pool *db.DB, log *slog.Logger) {
	if pool.Dialect() != db.Postgres {
		return
	}
	t := time.NewTicker(12 * time.Hour)
	defer t.Stop()
	for {
		if err := EnsurePartitions(ctx, pool, time.Now().UTC()); err != nil && ctx.Err() == nil {
			log.Error("request log partition maintenance failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
