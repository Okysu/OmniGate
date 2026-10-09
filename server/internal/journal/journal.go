// Package journal is the local append-only journal of settlement work that
// could not be written to the database (ADR-0010): wallet settlements, usage
// counters, quota records and request logs. Items are appended as JSON lines
// (fsync'ed) to a file in the data directory and replayed in order by a
// background worker once the database is reachable again (and at startup).
// Handlers must be idempotent: an item may be replayed more than once (for
// example after a crash between its database commit and its acknowledgement).
//
// File format (one JSON object per line):
//
//	{"v":1,"seq":7,"kind":"settlement","at":"…","data":{…}}   an item
//	{"ack":7}                                                  item 7 is done
//
// The file is truncated once every item is acknowledged and compacted when
// acknowledged items dominate it. A torn last line (crash during a write) is
// ignored with a warning and cut off; other undecodable lines are copied to
// "<name>.corrupt" and reported. Items that keep failing while the database
// is reachable are moved to "<name>.dead" with their last error (never
// dropped silently).
//
// The journal is for a single instance: two processes must not share a data
// directory.
package journal

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// FileName is the journal file in the data directory.
const FileName = "settlement-journal.jsonl"

// MaxFailures is how many times an item may fail while the database is
// reachable before it is moved to the dead-letter file.
const MaxFailures = 5

var (
	metricPending = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "omnigate_journal_pending", Help: "Settlement journal items waiting to be replayed into the database.",
	})
	metricReplayed = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "omnigate_journal_replayed_total", Help: "Settlement journal items replayed into the database, by kind.",
	}, []string{"kind"})
	metricFailures = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "omnigate_settlement_failures_total",
		Help: "Settlement work (kind settlement: wallet, usage counters, quota; kind request_logs: a request log batch) that failed after in-process retries and was journaled.",
	}, []string{"kind"})
	metricDead = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "omnigate_journal_dead_total", Help: "Settlement journal items moved to the dead-letter file, by kind.",
	}, []string{"kind"})
)

// Failed counts settlement work of kind that failed after its in-process
// retries (omnigate_settlement_failures_total).
func Failed(kind string) { metricFailures.WithLabelValues(kind).Inc() }

// Handler applies one item's data to the database. It must be idempotent.
type Handler func(ctx context.Context, data json.RawMessage) error

// Item is one journaled unit of work.
type Item struct {
	V    int             `json:"v"`
	Seq  uint64          `json:"seq"`
	Kind string          `json:"kind"`
	At   time.Time       `json:"at"`
	Data json.RawMessage `json:"data"`

	failures int
}

type ackLine struct {
	Ack uint64 `json:"ack"`
}

// Journal is the append-only settlement journal. All methods are safe for
// concurrent use.
type Journal struct {
	path string
	log  *slog.Logger

	// Ping reports whether the database is reachable; it decides whether a
	// failing item counts towards MaxFailures (set before Run / Replay).
	Ping func(ctx context.Context) error

	mu       sync.Mutex
	f        *os.File
	pending  []*Item
	seq      uint64
	lines    int // lines in the file (items + acks)
	handlers map[string]Handler
	closed   bool

	replayMu sync.Mutex // one replay at a time
}

// Open opens (creating dir and file when missing, mode 0700 / 0600) the
// journal in dir and loads the items that are not acknowledged yet.
func Open(dir string, log *slog.Logger) (*Journal, error) {
	if dir == "" {
		return nil, errors.New("journal: empty data directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("journal: create data directory: %w", err)
	}
	j := &Journal{path: filepath.Join(dir, FileName), log: log, handlers: map[string]Handler{}}
	f, err := os.OpenFile(j.path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("journal: %w", err)
	}
	j.f = f
	if err := j.load(); err != nil {
		f.Close()
		return nil, err
	}
	metricPending.Set(float64(len(j.pending)))
	if n := len(j.pending); n > 0 {
		log.Warn("settlement journal has pending items; they are replayed when the database is reachable", "pending", n, "path", j.path)
	}
	return j, nil
}

// Path returns the journal file path.
func (j *Journal) Path() string { return j.path }

// load reads the file: items minus acks become pending. A torn last line is
// cut off; undecodable complete lines go to the .corrupt file.
func (j *Journal) load() error {
	raw, err := io.ReadAll(j.f)
	if err != nil {
		return fmt.Errorf("journal: read: %w", err)
	}
	valid := len(raw)
	if i := bytes.LastIndexByte(raw, '\n'); i < len(raw)-1 {
		// The last line has no newline: a write was cut short (crash or
		// full disk). Its item was never acknowledged to the caller as
		// journaled, so it is ignored.
		valid = i + 1
		j.log.Warn("settlement journal: ignoring a partial last line", "path", j.path, "bytes", len(raw)-valid)
	}
	items := map[uint64]*Item{}
	var order []uint64
	var corrupt [][]byte
	sc := bufio.NewScanner(bytes.NewReader(raw[:valid]))
	sc.Buffer(make([]byte, 0, 64<<10), 64<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		j.lines++
		var probe struct {
			Ack *uint64 `json:"ack"`
			Seq uint64  `json:"seq"`
		}
		if err := json.Unmarshal(line, &probe); err != nil {
			corrupt = append(corrupt, append([]byte(nil), line...))
			continue
		}
		if probe.Ack != nil {
			delete(items, *probe.Ack)
			j.seq = max(j.seq, *probe.Ack)
			continue
		}
		var it Item
		if err := json.Unmarshal(line, &it); err != nil || it.Seq == 0 || it.Kind == "" {
			corrupt = append(corrupt, append([]byte(nil), line...))
			continue
		}
		if _, dup := items[it.Seq]; !dup {
			order = append(order, it.Seq)
		}
		items[it.Seq] = &it
		j.seq = max(j.seq, it.Seq)
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("journal: scan: %w", err)
	}
	for _, s := range order {
		if it, ok := items[s]; ok {
			j.pending = append(j.pending, it)
			delete(items, s)
		}
	}
	if len(corrupt) > 0 {
		j.log.Error("settlement journal: undecodable lines moved to the .corrupt file; inspect them manually",
			"path", j.path, "lines", len(corrupt))
		if err := appendLines(j.path+".corrupt", corrupt); err != nil {
			return fmt.Errorf("journal: save corrupt lines: %w", err)
		}
	}
	if valid != len(raw) || len(corrupt) > 0 {
		// Rewrite without the torn tail and the corrupt lines so that new
		// appends start on a clean line.
		return j.rewriteLocked()
	}
	_, err = j.f.Seek(0, io.SeekEnd)
	return err
}

// appendLines appends lines to the file at path (created 0600) and syncs it.
func appendLines(path string, lines [][]byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	for _, l := range lines {
		buf.Write(l)
		buf.WriteByte('\n')
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Handle registers the handler of kind (before Run / Replay).
func (j *Journal) Handle(kind string, h Handler) {
	j.mu.Lock()
	j.handlers[kind] = h
	j.mu.Unlock()
}

// ErrClosed is returned by Append after Close.
var ErrClosed = errors.New("journal: closed")

// Append durably records one item of kind (v is encoded as JSON). When it
// returns nil the item is on disk (fsync'ed) and will be replayed.
func (j *Journal) Append(kind string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return ErrClosed
	}
	it := &Item{V: 1, Seq: j.seq + 1, Kind: kind, At: time.Now().UTC(), Data: data}
	line, err := json.Marshal(it)
	if err != nil {
		return err
	}
	if err := j.writeLocked(line); err != nil {
		return err
	}
	j.seq = it.Seq
	j.pending = append(j.pending, it)
	metricPending.Set(float64(len(j.pending)))
	return nil
}

// writeLocked appends one line and syncs the file. On a failed write the
// file is cut back so that a torn line never precedes the next one.
func (j *Journal) writeLocked(line []byte) error {
	off, err := j.f.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	if _, err := j.f.Write(append(line, '\n')); err != nil {
		_ = j.f.Truncate(off)
		return fmt.Errorf("journal: write: %w", err)
	}
	if err := j.f.Sync(); err != nil {
		return fmt.Errorf("journal: sync: %w", err)
	}
	j.lines++
	return nil
}

// Pending returns the number of items not replayed yet.
func (j *Journal) Pending() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.pending)
}

// ack removes item seq from the pending list and records it on disk.
func (j *Journal) ack(seq uint64) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	for i, it := range j.pending {
		if it.Seq == seq {
			j.pending = append(j.pending[:i], j.pending[i+1:]...)
			break
		}
	}
	metricPending.Set(float64(len(j.pending)))
	if j.closed {
		return ErrClosed
	}
	if len(j.pending) == 0 {
		// Everything is applied: start over with an empty file.
		if err := j.f.Truncate(0); err != nil {
			return err
		}
		if _, err := j.f.Seek(0, io.SeekStart); err != nil {
			return err
		}
		j.lines = 0
		return j.f.Sync()
	}
	line, _ := json.Marshal(ackLine{Ack: seq})
	if err := j.writeLocked(line); err != nil {
		return err
	}
	if j.lines > 1000 && j.lines > 4*len(j.pending) {
		return j.rewriteLocked()
	}
	return nil
}

// rewriteLocked atomically replaces the file with the pending items only.
func (j *Journal) rewriteLocked() error {
	tmp := j.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	for _, it := range j.pending {
		line, err := json.Marshal(it)
		if err != nil {
			f.Close()
			return err
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, j.path); err != nil {
		return err
	}
	if d, err := os.Open(filepath.Dir(j.path)); err == nil {
		_ = d.Sync()
		d.Close()
	}
	nf, err := os.OpenFile(j.path, os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	if _, err := nf.Seek(0, io.SeekEnd); err != nil {
		nf.Close()
		return err
	}
	j.f.Close()
	j.f = nf
	j.lines = len(j.pending)
	return nil
}

// dead moves an item that keeps failing to the dead-letter file.
func (j *Journal) dead(it *Item, cause error) error {
	line, err := json.Marshal(struct {
		*Item
		Error  string    `json:"error"`
		DiedAt time.Time `json:"diedAt"`
	}{it, cause.Error(), time.Now().UTC()})
	if err != nil {
		return err
	}
	if err := appendLines(j.path+".dead", [][]byte{line}); err != nil {
		return err
	}
	metricDead.WithLabelValues(it.Kind).Inc()
	j.log.Error("settlement journal item keeps failing; moved to the dead-letter file for manual repair",
		"kind", it.Kind, "seq", it.Seq, "path", j.path+".dead", "err", cause)
	return j.ack(it.Seq)
}

// Replay applies the pending items in order and returns how many were
// applied. It stops at the first item that fails while the database is
// unreachable (or Ping is unset); an item that fails MaxFailures times while
// the database is reachable is moved to the dead-letter file.
func (j *Journal) Replay(ctx context.Context) (int, error) {
	j.replayMu.Lock()
	defer j.replayMu.Unlock()
	j.mu.Lock()
	items := append([]*Item(nil), j.pending...)
	j.mu.Unlock()
	done := 0
	for _, it := range items {
		if err := ctx.Err(); err != nil {
			return done, err
		}
		j.mu.Lock()
		h := j.handlers[it.Kind]
		j.mu.Unlock()
		var err error
		if h == nil {
			err = fmt.Errorf("no handler for journal item kind %q", it.Kind)
		} else {
			hctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			err = h(hctx, it.Data)
			cancel()
		}
		if err == nil {
			if aerr := j.ack(it.Seq); aerr != nil {
				return done, fmt.Errorf("journal: acknowledge item %d: %w", it.Seq, aerr)
			}
			metricReplayed.WithLabelValues(it.Kind).Inc()
			done++
			continue
		}
		if h == nil || j.reachable(ctx) {
			it.failures++
			j.log.Warn("settlement journal item failed", "kind", it.Kind, "seq", it.Seq, "failures", it.failures, "err", err)
			if it.failures >= MaxFailures || h == nil {
				if derr := j.dead(it, err); derr != nil {
					return done, fmt.Errorf("journal: dead-letter item %d: %w", it.Seq, derr)
				}
				continue
			}
		}
		return done, err
	}
	return done, nil
}

func (j *Journal) reachable(ctx context.Context) bool {
	if j.Ping == nil {
		return false
	}
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return j.Ping(pctx) == nil
}

// Run replays pending items at once and then every interval (backing off to
// a minute while replays fail) until ctx is done.
func (j *Journal) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	wait, backoff := time.Duration(0), interval
	for {
		if wait > 0 {
			t := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-t.C:
			}
		}
		wait = interval
		if j.Pending() == 0 {
			continue
		}
		n, err := j.Replay(ctx)
		if n > 0 {
			j.log.Info("settlement journal replayed", "items", n, "pending", j.Pending())
		}
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// The database is likely still down: back off up to a minute.
			backoff = min(2*backoff, max(time.Minute, interval))
			wait = backoff
			j.log.Warn("settlement journal replay paused", "pending", j.Pending(), "retry_in", wait, "err", err)
		} else {
			backoff = interval
		}
	}
}

// Close syncs and closes the file. Append fails afterwards; pending items
// stay on disk for the next start.
func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil
	}
	j.closed = true
	err := j.f.Sync()
	if cerr := j.f.Close(); err == nil {
		err = cerr
	}
	return err
}
