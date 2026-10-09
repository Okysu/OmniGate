package plugin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/apperr"
	"omnigate/internal/money"
	"omnigate/internal/platform/db"
	"omnigate/internal/plugin/engine"
)

// HookTimeout bounds each request hook (phase2-api.md §4.2).
const HookTimeout = 50 * time.Millisecond

// Loaded is an immutable plugin version ready to run.
type Loaded struct {
	VersionID uuid.UUID
	PluginID  uuid.UUID
	Key       string
	Name      string
	Version   string
	Approval  string
	Builtin   bool
	Manifest  *Manifest
	prog      *engine.Program // nil for builtin plugins
	svc       *Service
}

// Loaded returns a cached immutable version.
func (s *Service) Loaded(ctx context.Context, versionID uuid.UUID) (*Loaded, error) {
	s.mu.Lock()
	if l, ok := s.loaded[versionID]; ok {
		s.mu.Unlock()
		return l, nil
	}
	s.mu.Unlock()
	var l Loaded
	var manifest []byte
	var bundle *string
	var hash, source, status string
	err := s.pool.QueryRow(ctx, `SELECT v.id, v.plugin_id, p.plugin_key, p.name, v.version, v.approval, v.manifest, v.bundle,
		v.content_hash, p.source, p.status FROM plugin_versions v JOIN plugins p ON p.id = v.plugin_id WHERE v.id = $1`, versionID).
		Scan(&l.VersionID, &l.PluginID, &l.Key, &l.Name, &l.Version, &l.Approval, &manifest, &bundle, &hash, &source, &status)
	if db.IsNoRows(err) {
		return nil, apperr.NotFound("插件版本")
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(manifest, &l.Manifest); err != nil {
		return nil, err
	}
	l.Builtin, l.svc = source == "builtin", s
	if bundle != nil && *bundle != "" {
		if l.prog, err = s.engine.Program(hash, *bundle); err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.status[l.PluginID]; !ok {
		s.status[l.PluginID] = status
	}
	s.hashOwns[hash] = l.PluginID
	if l.Approval == "approved" { // only cache final states
		s.loaded[versionID] = &l
	}
	return &l, nil
}

// Enabled reports whether a plugin is enabled (cached; refreshed on change).
func (s *Service) Enabled(pluginID uuid.UUID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status[pluginID] != "disabled"
}

// RefreshStatus reloads plugin statuses from the database.
func (s *Service) RefreshStatus(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `SELECT id, status FROM plugins`)
	if err != nil {
		return err
	}
	defer rows.Close()
	st := map[uuid.UUID]string{}
	for rows.Next() {
		var id uuid.UUID
		var v string
		if err := rows.Scan(&id, &v); err != nil {
			return err
		}
		st[id] = v
	}
	s.mu.Lock()
	s.status = st
	s.mu.Unlock()
	return rows.Err()
}

// ChannelEnv is what a plugin may know about the channel it runs for.
type ChannelEnv struct {
	ID           uuid.UUID
	Name         string
	BaseURL      string
	Models       []ModelDefault
	Config       map[string]any
	Secrets      map[string]string // "apiKey" plus x-secret fields
	AllowPrivate bool
	HTTP         engine.HTTPDoer
	UserAgent    string
}

func (l *Loaded) env(ch ChannelEnv, capability string, mocks []engine.MockFetch) *engine.Env {
	m := l.Manifest
	secrets := map[string]string{}
	for _, name := range m.Permissions.Secrets {
		if v, ok := ch.Secrets[name]; ok && v != "" {
			secrets[name] = v
		}
	}
	host := ""
	if u, err := url.Parse(ch.BaseURL); err == nil {
		host = u.Hostname()
	}
	env := &engine.Env{Network: m.Permissions.Network, BaseHost: host, AllowHTTP: ch.AllowPrivate, HTTP: ch.HTTP, Mocks: mocks,
		Secrets: secrets, UserAgent: ch.UserAgent}
	models := ch.Models
	if models == nil {
		models = []ModelDefault{}
	}
	cfg := ch.Config
	if cfg == nil {
		cfg = map[string]any{}
	}
	env.Context = map[string]any{
		"channel":    map[string]any{"id": ch.ID.String(), "name": ch.Name, "baseUrl": ch.BaseURL, "models": models},
		"config":     cfg,
		"capability": capability,
		"now":        time.Now().UTC().Format(time.RFC3339),
	}
	if m.Permissions.Storage != nil && ch.ID != uuid.Nil {
		env.Storage = &pgStorage{pool: l.svc.pool, pluginID: l.PluginID, channelID: ch.ID, quota: *m.Permissions.Storage}
	}
	return env
}

// CapabilityResult is the persisted outcome of a capability run.
type CapabilityResult struct {
	OK            bool                 `json:"ok"`
	Unsupported   bool                 `json:"unsupported"`
	Output        json.RawMessage      `json:"output"`
	Error         *string              `json:"error"`
	DurationMs    int64                `json:"durationMs"`
	FetchedAt     time.Time            `json:"fetchedAt"`
	PluginVersion string               `json:"pluginVersion"`
	Logs          []engine.LogLine     `json:"logs,omitempty"`
	Fetches       []engine.FetchRecord `json:"fetches,omitempty"`
}

// CallCapability runs a declared capability and validates its output.
func (l *Loaded) CallCapability(ctx context.Context, ch ChannelEnv, name string, input any, mocks []engine.MockFetch) *CapabilityResult {
	res := &CapabilityResult{FetchedAt: time.Now().UTC(), PluginVersion: l.Version}
	fail := func(msg string) *CapabilityResult {
		res.Error = &msg
		return res
	}
	decl, ok := l.Manifest.Capabilities[name]
	if !ok {
		return fail("插件没有声明能力 " + name)
	}
	if l.prog == nil {
		return fail("内置插件没有可执行的能力")
	}
	env := l.env(ch, name, mocks)
	r, err := l.prog.Call(ctx, env, []string{"capabilities", name}, decl.TimeoutDuration(), input, env.Context)
	if r != nil {
		res.DurationMs, res.Logs, res.Fetches = r.DurationMs, r.Logs, r.Fetches
	}
	if err != nil {
		var pe *engine.Error
		if errors.As(err, &pe) {
			return fail(pe.Message)
		}
		return fail(err.Error())
	}
	var un struct {
		Unsupported bool   `json:"unsupported"`
		Reason      string `json:"reason"`
	}
	if json.Unmarshal(r.Output, &un) == nil && un.Unsupported {
		res.OK, res.Unsupported = true, true
		res.Output = r.Output
		return res
	}
	if msg := validateOutput(decl.Output, r.Output); msg != "" {
		return fail("输出不符合 " + decl.Output + " 格式：" + msg)
	}
	res.OK, res.Output = true, r.Output
	return res
}

func validateOutput(kind string, raw json.RawMessage) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "不是合法 JSON"
	}
	obj, isObj := v.(map[string]any)
	str := func(k string, required bool) string {
		x, ok := obj[k]
		if !ok || x == nil {
			if required {
				return "缺少字段 " + k
			}
			return ""
		}
		s, ok := x.(string)
		if !ok {
			return k + " 必须是字符串"
		}
		if (k == "total" || k == "granted" || k == "toppedUp") && s != "" {
			if _, err := money.Parse(s); err != nil {
				return k + " 必须是十进制金额字符串"
			}
		}
		return ""
	}
	switch kind {
	case "json":
		return ""
	case "balance":
		if !isObj {
			return "必须是对象"
		}
		for _, f := range []struct {
			k   string
			req bool
		}{{"currency", true}, {"total", true}, {"granted", false}, {"toppedUp", false}} {
			if m := str(f.k, f.req); m != "" {
				return m
			}
		}
		if _, ok := obj["available"].(bool); !ok {
			return "available 必须是布尔值"
		}
	case "quota":
		ws, ok := obj["windows"].([]any)
		if !isObj || !ok {
			return "需要 windows 数组"
		}
		for _, w := range ws {
			m, ok := w.(map[string]any)
			if !ok || m["id"] == nil || m["label"] == nil || m["used"] == nil || m["limit"] == nil {
				return "windows 中每项需要 id、label、used、limit"
			}
		}
	case "models":
		ms, ok := obj["models"].([]any)
		if !isObj || !ok {
			return "需要 models 数组"
		}
		for _, x := range ms {
			if m, ok := x.(map[string]any); !ok || m["id"] == nil {
				return "models 中每项需要 id"
			}
		}
	case "usage":
		if _, ok := obj["periods"].([]any); !isObj || !ok {
			return "需要 periods 数组"
		}
	case "health":
		if _, ok := obj["ok"].(bool); !isObj || !ok {
			return "需要布尔字段 ok"
		}
	}
	return ""
}

// UpstreamRequest is the hook payload (phase2-api.md §4.2).
type UpstreamRequest struct {
	Dialect string            `json:"dialect"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
}

// RunHooks applies the plugin's declared request hooks.
func (l *Loaded) RunHooks(ctx context.Context, ch ChannelEnv, req UpstreamRequest) (*UpstreamRequest, error) {
	return l.runHooks(ctx, ch, req, knownHooks)
}

// RunSignHook applies only signRequest. It is used for requests whose body is
// not JSON (multipart image uploads, phase7-api.md §1): the hook receives an
// empty object as body (so hooks written for JSON bodies keep working) and
// may change path and headers; a returned body is ignored.
func (l *Loaded) RunSignHook(ctx context.Context, ch ChannelEnv, req UpstreamRequest) (*UpstreamRequest, error) {
	req.Body = json.RawMessage("{}")
	out, err := l.runHooks(ctx, ch, req, []string{"signRequest"})
	if err != nil {
		return nil, err
	}
	out.Body = nil
	return out, nil
}

func (l *Loaded) runHooks(ctx context.Context, ch ChannelEnv, req UpstreamRequest, allowed []string) (*UpstreamRequest, error) {
	var hooks []string
	for _, h := range allowed {
		if l.Manifest.HasHook(h) {
			hooks = append(hooks, h)
		}
	}
	if len(hooks) == 0 || l.prog == nil {
		return &req, nil
	}
	env := l.env(ch, "", nil)
	r, err := l.prog.RunHooks(ctx, env, hooks, req, HookTimeout)
	if err != nil {
		return nil, err
	}
	var out UpstreamRequest
	if err := json.Unmarshal(r.Output, &out); err != nil {
		return nil, &engine.Error{Kind: "output", Message: "Hook 返回值不是请求对象"}
	}
	if !strings.HasPrefix(out.Path, "/") || strings.Contains(out.Path, "://") || strings.HasPrefix(out.Path, "//") {
		return nil, &engine.Error{Kind: "output", Message: "Hook 返回的 path 必须以 / 开头且不能包含主机"}
	}
	if len(out.Body) == 0 {
		out.Body = req.Body
	}
	return &out, nil
}

// SignsRequests reports whether the plugin takes over upstream authentication.
func (l *Loaded) SignsRequests() bool { return l.prog != nil && l.Manifest.HasHook("signRequest") }

// HasHooks reports whether any request hook is declared.
func (l *Loaded) HasHooks() bool {
	return l.prog != nil && slices.ContainsFunc(knownHooks, l.Manifest.HasHook)
}

// MigrateConfig runs migrateConfig(oldConfig, fromVersion) when exported.
func (l *Loaded) MigrateConfig(ctx context.Context, ch ChannelEnv, old map[string]any, fromVersion string) (map[string]any, error) {
	if l.prog == nil || !l.prog.Has("migrateConfig") {
		return old, nil
	}
	r, err := l.prog.Call(ctx, l.env(ch, "", nil), []string{"migrateConfig"}, 2*time.Second, old, fromVersion)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(r.Output, &out); err != nil || out == nil {
		return nil, &engine.Error{Kind: "output", Message: "migrateConfig 必须返回配置对象"}
	}
	return out, nil
}

// ---- capability results ----

func (s *Service) SaveResult(ctx context.Context, channelID uuid.UUID, versionID uuid.UUID, name string, r *CapabilityResult) error {
	var errText *string
	if r.Error != nil {
		errText = r.Error
	}
	var out []byte
	if len(r.Output) > 0 {
		out = r.Output
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO capability_results (channel_id, capability, ok, unsupported, output, error, plugin_version_id, duration_ms, fetched_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (channel_id, capability) DO UPDATE SET ok = EXCLUDED.ok, unsupported = EXCLUDED.unsupported, output = EXCLUDED.output,
		error = EXCLUDED.error, plugin_version_id = EXCLUDED.plugin_version_id, duration_ms = EXCLUDED.duration_ms, fetched_at = EXCLUDED.fetched_at`,
		channelID, name, r.OK, r.Unsupported, out, errText, versionID, r.DurationMs, r.FetchedAt)
	return err
}

func (s *Service) Results(ctx context.Context, channelID uuid.UUID) (map[string]*CapabilityResult, error) {
	rows, err := s.pool.Query(ctx, `SELECT r.capability, r.ok, r.unsupported, r.output, r.error, r.duration_ms, r.fetched_at, coalesce(v.version, '')
		FROM capability_results r LEFT JOIN plugin_versions v ON v.id = r.plugin_version_id WHERE r.channel_id = $1`, channelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*CapabilityResult{}
	for rows.Next() {
		var name string
		var r CapabilityResult
		var dur int32
		var output []byte
		if err := rows.Scan(&name, &r.OK, &r.Unsupported, &output, &r.Error, &dur, &r.FetchedAt, &r.PluginVersion); err != nil {
			return nil, err
		}
		r.Output, r.DurationMs = output, int64(dur)
		out[name] = &r
	}
	return out, rows.Err()
}

// ---- storage ----

type pgStorage struct {
	pool      *db.DB
	pluginID  uuid.UUID
	channelID uuid.UUID
	quota     StorageQuota
}

func (st *pgStorage) Get(ctx context.Context, key string) (json.RawMessage, bool, error) {
	var v []byte
	err := st.pool.QueryRow(ctx, `SELECT value FROM plugin_storage WHERE plugin_id = $1 AND channel_id = $2 AND key = $3`, st.pluginID, st.channelID, key).Scan(&v)
	if db.IsNoRows(err) {
		return nil, false, nil
	}
	return v, err == nil, err
}

func (st *pgStorage) Set(ctx context.Context, key string, value json.RawMessage) error {
	if len(key) == 0 || len(key) > 200 {
		return errors.New("存储键长度应为 1–200")
	}
	var keys, bytes int
	if err := st.pool.QueryRow(ctx, `SELECT count(*), coalesce(sum(length(value::text)), 0) FROM plugin_storage
		WHERE plugin_id = $1 AND channel_id = $2 AND key <> $3`, st.pluginID, st.channelID, key).Scan(&keys, &bytes); err != nil {
		return err
	}
	if keys+1 > st.quota.MaxKeys || bytes+len(value) > st.quota.MaxBytes {
		return fmt.Errorf("超出存储配额（%d 个键 / %d 字节）", st.quota.MaxKeys, st.quota.MaxBytes)
	}
	_, err := st.pool.Exec(ctx, `INSERT INTO plugin_storage (plugin_id, channel_id, key, value) VALUES ($1, $2, $3, $4)
		ON CONFLICT (plugin_id, channel_id, key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, st.pluginID, st.channelID, key, value)
	return err
}

func (st *pgStorage) Delete(ctx context.Context, key string) error {
	_, err := st.pool.Exec(ctx, `DELETE FROM plugin_storage WHERE plugin_id = $1 AND channel_id = $2 AND key = $3`, st.pluginID, st.channelID, key)
	return err
}

// ---- editor test runner ----

// TestCase is a mock test (phase2-api.md §6, phase9-api.md §2–§3).
//
//   - capability: input → capability output (fetch mocks)
//   - hook transformRequest / signRequest: request (UpstreamRequest) → request
//   - hook buildRequest: request (Chat Completions request) → upstream request
//     after signRequest, with secret handles replaced
//   - hook parseResponse / normalizeError: response → CanonicalResponse /
//     {status, message}
//   - hook parseStream (or endStream): chunks / chunksBase64 are fed one call
//     each to parseStream in one session, then endStream; the output is the
//     concatenated event array
//   - meter: usage + billingCtx → units (decimal string)
type TestCase struct {
	Name         string             `json:"name,omitempty"`
	Case         string             `json:"case,omitempty"` // path of a tests/*.json file in the draft
	Capability   string             `json:"capability,omitempty"`
	Hook         string             `json:"hook,omitempty"`
	Meter        string             `json:"meter,omitempty"`
	Input        json.RawMessage    `json:"input,omitempty"`
	Request      json.RawMessage    `json:"request,omitempty"`
	Response     *TestResponse      `json:"response,omitempty"`
	Chunks       []string           `json:"chunks,omitempty"`
	ChunksBase64 []string           `json:"chunksBase64,omitempty"`
	Usage        json.RawMessage    `json:"usage,omitempty"`
	BillingCtx   *BillingCtx        `json:"billingCtx,omitempty"`
	Config       map[string]any     `json:"config,omitempty"`
	Secrets      map[string]string  `json:"secrets,omitempty"`
	BaseURL      string             `json:"baseUrl,omitempty"`
	Fetch        []engine.MockFetch `json:"fetch,omitempty"`
	Expect       *struct {
		Output json.RawMessage `json:"output,omitempty"`
		Error  string          `json:"error,omitempty"`
	} `json:"expect,omitempty"`
}

// TestResponse is a mocked upstream response; body may be a string or any
// JSON value (sent as its JSON text).
type TestResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    json.RawMessage   `json:"body,omitempty"`
}

func (r *TestResponse) upstream() UpstreamResponse {
	status := r.Status
	if status == 0 {
		status = 200
	}
	h := map[string]string{}
	for k, v := range r.Headers {
		h[strings.ToLower(k)] = v
	}
	body := ""
	if b := bytes.TrimSpace(r.Body); len(b) > 0 {
		if json.Unmarshal(b, &body) != nil {
			body = string(b)
			if _, ok := h["content-type"]; !ok {
				h["content-type"] = "application/json"
			}
		}
	}
	return UpstreamResponse{Status: status, Headers: h, Body: body}
}

type TestResult struct {
	OK          bool                 `json:"ok"`
	Output      json.RawMessage      `json:"output"`
	Error       *string              `json:"error"`
	Logs        []engine.LogLine     `json:"logs"`
	Fetches     []engine.FetchRecord `json:"fetches"`
	DurationMs  int64                `json:"durationMs"`
	Expectation *string              `json:"expectation"` // why the expectation failed, if any
	Build       *BuildResult         `json:"build,omitempty"`
	// Streaming cases (hook parseStream): one entry per parseStream /
	// endStream call, and the Chat Completions chunks a Chat client would
	// receive for the events (host conversion, phase9-api.md §2).
	Calls      []StreamCall      `json:"calls,omitempty"`
	ChatChunks []json.RawMessage `json:"chatChunks,omitempty"`
}

// StreamCall is one hook call of a streaming test case.
type StreamCall struct {
	Hook       string            `json:"hook"`  // parseStream | endStream
	Chunk      *int              `json:"chunk"` // input chunk index (null for endStream)
	DurationMs float64           `json:"durationMs"`
	Events     []json.RawMessage `json:"events"`
	Error      *string           `json:"error,omitempty"`
}

// testScale relaxes hook timeouts in the editor (cold runtimes, slow machines).
const testScale = 4

// RunTest executes a mock test against package files (never touching the network).
func (s *Service) RunTest(ctx context.Context, files map[string]string, tc TestCase) (*TestResult, error) {
	if tc.Case != "" {
		raw, ok := files[tc.Case]
		if !ok {
			return nil, apperr.Validation("测试用例文件不存在："+tc.Case, nil)
		}
		if err := json.Unmarshal([]byte(raw), &tc); err != nil {
			return nil, apperr.Validation("测试用例不是合法 JSON："+err.Error(), nil)
		}
	}
	b := Build(files, false)
	if !b.OK {
		return &TestResult{Build: b, Logs: []engine.LogLine{}, Fetches: []engine.FetchRecord{}}, nil
	}
	key := "test-" + b.hash
	prog, err := s.engine.Program(key, b.bundle)
	if err != nil {
		return nil, err
	}
	defer s.engine.Drop(key)
	l := &Loaded{Manifest: b.Manifest, prog: prog, svc: s, Version: b.Manifest.Version}
	mocks := tc.Fetch
	if mocks == nil {
		mocks = []engine.MockFetch{}
	}
	baseURL := tc.BaseURL
	if baseURL == "" {
		baseURL = b.Manifest.Defaults.BaseURL
	}
	ch := ChannelEnv{Name: "测试渠道", BaseURL: baseURL, Config: tc.Config, Secrets: tc.Secrets}
	out := &TestResult{Logs: []engine.LogLine{}, Fetches: []engine.FetchRecord{}}
	setErr := func(err error) {
		msg := err.Error()
		var pe *engine.Error
		var me *MeterError
		switch {
		case errors.As(err, &pe):
			msg = pe.Message
		case errors.As(err, &me):
			msg = me.Message
		}
		out.Error = &msg
	}
	switch {
	case tc.Capability != "":
		var input any
		if len(tc.Input) > 0 {
			input = tc.Input
		}
		r := l.CallCapability(ctx, ch, tc.Capability, input, mocks)
		out.Output, out.Error, out.DurationMs = r.Output, r.Error, r.DurationMs
		if r.Logs != nil {
			out.Logs = r.Logs
		}
		if r.Fetches != nil {
			out.Fetches = r.Fetches
		}
	case tc.Meter != "":
		if _, ok := l.Manifest.Meter(tc.Meter); !ok {
			return nil, apperr.Validation("manifest 没有声明计量 "+tc.Meter, nil)
		}
		usage := json.RawMessage("{}")
		if len(tc.Usage) > 0 {
			usage = tc.Usage
		}
		bc := BillingCtx{}
		if tc.BillingCtx != nil {
			bc = *tc.BillingCtx
		}
		start := time.Now()
		units, err := l.computeUnits(ctx, tc.Meter, usage, bc, MeterTimeout*testScale)
		out.DurationMs = time.Since(start).Milliseconds()
		if err != nil {
			setErr(err)
		} else {
			out.Output, _ = json.Marshal(units)
		}
	case tc.Hook != "":
		if err := s.runHookTest(ctx, l, ch, mocks, tc, out); err != nil {
			return nil, err
		}
	default:
		return nil, apperr.Validation("用例需要 capability、hook 或 meter", nil)
	}
	out.OK = out.Error == nil
	if tc.Expect != nil {
		switch {
		case tc.Expect.Error != "":
			if out.Error == nil || !strings.Contains(*out.Error, tc.Expect.Error) {
				m := fmt.Sprintf("期望错误包含 %q", tc.Expect.Error)
				out.Expectation, out.OK = &m, false
			} else {
				out.OK = true
			}
		case len(tc.Expect.Output) > 0:
			var want, got any
			_ = json.Unmarshal(tc.Expect.Output, &want)
			_ = json.Unmarshal(out.Output, &got)
			if path, ok := subsetMatch(want, got, ""); !ok {
				m := "输出与期望不符：" + path
				out.Expectation, out.OK = &m, false
			}
		}
	}
	return out, nil
}

// runHookTest runs a request hook or custom-protocol hook test case.
func (s *Service) runHookTest(ctx context.Context, l *Loaded, ch ChannelEnv, mocks []engine.MockFetch, tc TestCase, out *TestResult) error {
	fail := func(err error) {
		msg := err.Error()
		var pe *engine.Error
		if errors.As(err, &pe) {
			msg = pe.Message
		}
		out.Error = &msg
	}
	if slices.Contains(knownHooks, tc.Hook) {
		var req UpstreamRequest
		if len(tc.Request) == 0 || json.Unmarshal(tc.Request, &req) != nil {
			return apperr.Validation("hook 用例需要合法的 hook 名称与 request", nil)
		}
		env := l.env(ch, "", mocks)
		r, err := l.prog.RunHooks(ctx, env, []string{tc.Hook}, req, HookTimeout*testScale)
		if r != nil {
			out.Output, out.Logs, out.Fetches, out.DurationMs = r.Output, r.Logs, r.Fetches, r.DurationMs
		}
		if err != nil {
			fail(err)
		}
		return nil
	}
	if !slices.Contains(protocolHooks, tc.Hook) {
		return apperr.Validation("未知的 hook："+tc.Hook, nil)
	}
	if !l.Manifest.CustomProtocol() || !l.Manifest.HasHook(tc.Hook) {
		return apperr.Validation("插件没有声明 Hook "+tc.Hook+"（需要 protocol: \"custom\"）", nil)
	}
	ps, err := l.newProtocolSession(ctx, ch, mocks, ProtocolBudget*testScale, testScale)
	if err != nil {
		fail(err)
		return nil
	}
	defer func() {
		out.Logs, out.Fetches, out.DurationMs = ps.Logs(), ps.Fetches(), ps.Used().Milliseconds()
		ps.Close()
	}()
	switch tc.Hook {
	case "buildRequest":
		if len(tc.Request) == 0 {
			return apperr.Validation("buildRequest 用例需要 request（Chat Completions 请求）", nil)
		}
		_, raw, err := ps.BuildRequest(ctx, tc.Request)
		if err != nil {
			fail(err)
			return nil
		}
		out.Output = raw
	case "parseResponse", "normalizeError":
		if tc.Response == nil {
			return apperr.Validation(tc.Hook+" 用例需要 response", nil)
		}
		var raw json.RawMessage
		if tc.Hook == "parseResponse" {
			raw, err = ps.ParseResponse(ctx, tc.Response.upstream())
		} else {
			_, raw, err = ps.NormalizeError(ctx, tc.Response.upstream())
		}
		out.Output = raw
		if err != nil {
			fail(err)
		}
	case "parseStream", "endStream":
		chunks := make([][]byte, 0, len(tc.Chunks)+len(tc.ChunksBase64))
		for _, c := range tc.Chunks {
			chunks = append(chunks, []byte(c))
		}
		for _, c := range tc.ChunksBase64 {
			b, err := base64.StdEncoding.DecodeString(c)
			if err != nil {
				return apperr.Validation("chunksBase64 不是合法的 base64", nil)
			}
			chunks = append(chunks, b)
		}
		st := newStreamTest(out)
		for i, c := range chunks {
			before := ps.Used()
			raw, err := ps.ParseStream(ctx, c)
			if !st.call("parseStream", &i, ps.Used()-before, raw, err) {
				return nil
			}
		}
		if l.Manifest.HasHook("endStream") {
			before := ps.Used()
			raw, err := ps.EndStream(ctx)
			if !st.call("endStream", nil, ps.Used()-before, raw, err) {
				return nil
			}
		}
		st.finish()
	}
	return nil
}

// subsetMatch reports whether want is contained in got (objects: subset of keys).
func subsetMatch(want, got any, at string) (string, bool) {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return at + " 应为对象", false
		}
		for k, v := range w {
			if p, ok := subsetMatch(v, g[k], at+"/"+k); !ok {
				return p, false
			}
		}
		return "", true
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) < len(w) {
			return at + " 数组长度不足", false
		}
		for i := range w {
			if p, ok := subsetMatch(w[i], g[i], fmt.Sprintf("%s/%d", at, i)); !ok {
				return p, false
			}
		}
		return "", true
	default:
		if fmt.Sprint(want) != fmt.Sprint(got) {
			return fmt.Sprintf("%s 期望 %v，实际 %v", at, want, got), false
		}
		return "", true
	}
}
