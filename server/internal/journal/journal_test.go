package journal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func open(t *testing.T, dir string) (*Journal, *logBuf) {
	t.Helper()
	lb := &logBuf{}
	j, err := Open(dir, slog.New(slog.NewTextHandler(lb, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	return j, lb
}

// recorder is a handler that records what it applied (and can fail).
type recorder struct {
	mu   sync.Mutex
	seen []string
	fail error
}

func (r *recorder) handle(_ context.Context, data json.RawMessage) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail != nil {
		return r.fail
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	r.seen = append(r.seen, s)
	return nil
}

func TestAppendReplayAndRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "data")
	j, _ := open(t, dir)
	st, err := os.Stat(dir)
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("data dir = %v %v", st, err)
	}
	if st, err := os.Stat(j.Path()); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("journal file = %v %v", st, err)
	}
	for _, s := range []string{"a", "b", "c"} {
		if err := j.Append("k", s); err != nil {
			t.Fatal(err)
		}
	}
	if j.Pending() != 3 {
		t.Fatalf("pending = %d", j.Pending())
	}
	// The database is down: nothing applied, order kept.
	r := &recorder{fail: errors.New("db down")}
	j.Handle("k", r.handle)
	if n, err := j.Replay(context.Background()); n != 0 || err == nil {
		t.Fatalf("replay while down = %d %v", n, err)
	}
	// A restart (new process on the same directory) finds the items.
	_ = j.Close()
	if err := j.Append("k", "late"); !errors.Is(err, ErrClosed) {
		t.Fatalf("append after close = %v", err)
	}
	j2, _ := open(t, dir)
	if j2.Pending() != 3 {
		t.Fatalf("pending after restart = %d", j2.Pending())
	}
	r2 := &recorder{}
	j2.Handle("k", r2.handle)
	if n, err := j2.Replay(context.Background()); n != 3 || err != nil {
		t.Fatalf("replay = %d %v", n, err)
	}
	if strings.Join(r2.seen, ",") != "a,b,c" || j2.Pending() != 0 {
		t.Fatalf("applied %v, pending %d", r2.seen, j2.Pending())
	}
	// Everything acknowledged: the file is empty, a second replay is a no-op.
	if st, _ := os.Stat(j2.Path()); st.Size() != 0 {
		t.Fatalf("journal size after replay = %d", st.Size())
	}
	if n, err := j2.Replay(context.Background()); n != 0 || err != nil {
		t.Fatalf("second replay = %d %v", n, err)
	}
	_ = j2.Close()
	j3, _ := open(t, dir)
	if j3.Pending() != 0 {
		t.Fatalf("pending after second restart = %d", j3.Pending())
	}
	// Sequence numbers keep growing after a restart.
	if err := j3.Append("k", "d"); err != nil || j3.pending[0].Seq != 1 {
		t.Fatalf("append = %v, seq %d", err, j3.pending[0].Seq)
	}
}

func TestPartialAckRestart(t *testing.T) {
	dir := t.TempDir()
	j, _ := open(t, dir)
	for _, s := range []string{"a", "b", "c"} {
		_ = j.Append("k", s)
	}
	// "b" keeps failing while the database is unreachable: a is applied,
	// replay stops at b and keeps c for later.
	calls := 0
	j.Handle("k", func(_ context.Context, data json.RawMessage) error {
		calls++
		if string(data) == `"b"` {
			return errors.New("down")
		}
		return nil
	})
	if n, err := j.Replay(context.Background()); n != 1 || err == nil || calls != 2 {
		t.Fatalf("replay = %d %v calls %d", n, err, calls)
	}
	_ = j.Close()
	j2, _ := open(t, dir)
	r := &recorder{}
	j2.Handle("k", r.handle)
	if n, err := j2.Replay(context.Background()); n != 2 || err != nil || strings.Join(r.seen, ",") != "b,c" {
		t.Fatalf("replay after restart = %d %v %v", n, err, r.seen)
	}
}

func TestTornLastLine(t *testing.T) {
	dir := t.TempDir()
	j, _ := open(t, dir)
	_ = j.Append("k", "a")
	_ = j.Close()
	// A crash in the middle of the next write leaves a partial line.
	f, err := os.OpenFile(filepath.Join(dir, FileName), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"v":1,"seq":2,"kind":"k","da`)
	f.Close()
	j2, lb := open(t, dir)
	if j2.Pending() != 1 || !strings.Contains(lb.String(), "partial last line") {
		t.Fatalf("pending = %d, log = %s", j2.Pending(), lb.String())
	}
	// New items start on a clean line.
	if err := j2.Append("k", "b"); err != nil {
		t.Fatal(err)
	}
	_ = j2.Close()
	j3, _ := open(t, dir)
	r := &recorder{}
	j3.Handle("k", r.handle)
	if n, err := j3.Replay(context.Background()); n != 2 || err != nil || strings.Join(r.seen, ",") != "a,b" {
		t.Fatalf("replay = %d %v %v", n, err, r.seen)
	}
}

func TestCorruptLineKept(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	content := `{"v":1,"seq":1,"kind":"k","at":"2026-01-01T00:00:00Z","data":"a"}` + "\n" +
		`garbage line` + "\n" +
		`{"v":1,"seq":2,"kind":"k","at":"2026-01-01T00:00:00Z","data":"b"}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	j, lb := open(t, dir)
	if j.Pending() != 2 || !strings.Contains(lb.String(), "undecodable lines") {
		t.Fatalf("pending = %d, log = %s", j.Pending(), lb.String())
	}
	if raw, err := os.ReadFile(path + ".corrupt"); err != nil || string(raw) != "garbage line\n" {
		t.Fatalf("corrupt file = %q %v", raw, err)
	}
}

func TestDeadLetter(t *testing.T) {
	dir := t.TempDir()
	j, _ := open(t, dir)
	j.Ping = func(context.Context) error { return nil } // the database is up
	_ = j.Append("k", "poison")
	_ = j.Append("k", "ok")
	_ = j.Append("unknown", "x")
	j.Handle("k", func(_ context.Context, data json.RawMessage) error {
		if string(data) == `"poison"` {
			return errors.New("constraint violation")
		}
		return nil
	})
	for i := 1; i < MaxFailures; i++ {
		if n, err := j.Replay(context.Background()); n != 0 || err == nil {
			t.Fatalf("replay %d = %d %v", i, n, err)
		}
	}
	// The last failure moves the poison item aside; the others proceed (an
	// unknown kind is dead-lettered at once).
	if n, err := j.Replay(context.Background()); n != 1 || err != nil || j.Pending() != 0 {
		t.Fatalf("final replay = %d %v pending %d", n, err, j.Pending())
	}
	raw, err := os.ReadFile(j.Path() + ".dead")
	if err != nil || strings.Count(string(raw), "\n") != 2 || !strings.Contains(string(raw), "constraint violation") ||
		!strings.Contains(string(raw), "no handler") {
		t.Fatalf("dead letters = %s %v", raw, err)
	}
}

func TestCompaction(t *testing.T) {
	dir := t.TempDir()
	j, _ := open(t, dir)
	j.Handle("k", func(context.Context, json.RawMessage) error { return nil })
	_ = j.Append("k", "keep") // never acknowledged below
	for i := 0; i < 1200; i++ {
		_ = j.Append("k", "x")
	}
	// Acknowledge everything but the first item directly.
	j.mu.Lock()
	items := append([]*Item(nil), j.pending[1:]...)
	j.mu.Unlock()
	for _, it := range items {
		if err := j.ack(it.Seq); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := os.ReadFile(j.Path())
	if lines := strings.Count(string(raw), "\n"); lines > 1100 {
		t.Fatalf("journal not compacted: %d lines", lines)
	}
	_ = j.Close()
	j2, _ := open(t, dir)
	if j2.Pending() != 1 {
		t.Fatalf("pending after compaction = %d", j2.Pending())
	}
}

func TestConcurrentAppend(t *testing.T) {
	j, _ := open(t, t.TempDir())
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := 0; k < 10; k++ {
				_ = j.Append("k", "v")
			}
		}()
	}
	wg.Wait()
	_ = j.Close()
	j2, _ := Open(filepath.Dir(j.Path()), slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer j2.Close()
	if j2.Pending() != 200 {
		t.Fatalf("pending = %d", j2.Pending())
	}
}
