package engine

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/dop251/goja"
)

// HTTPDoer performs upstream requests (the SSRF-guarded client).
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// Storage is the per-(plugin, channel) key/value store.
type Storage interface {
	Get(ctx context.Context, key string) (json.RawMessage, bool, error)
	Set(ctx context.Context, key string, value json.RawMessage) error
	Delete(ctx context.Context, key string) error
}

// MockFetch is a canned response for tests; when Env.Mocks is non-nil the
// network is never touched.
type MockFetch struct {
	Match struct {
		Method string `json:"method"`
		URL    string `json:"url"`
	} `json:"match"`
	Response struct {
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers,omitempty"`
		JSON    json.RawMessage   `json:"json,omitempty"`
		Body    string            `json:"body,omitempty"`
	} `json:"response"`
}

// Env is the host environment for one call.
type Env struct {
	Network   []string          // allowed hosts ("$baseUrl" resolved via BaseHost; "*.x.com")
	BaseHost  string            // host of the channel baseUrl
	AllowHTTP bool              // plain http allowed (private-network channels)
	HTTP      HTTPDoer          // nil disables real fetches
	Mocks     []MockFetch       // non-nil: only mocks are used
	Secrets   map[string]string // permitted secret name -> plaintext
	Storage   Storage           // nil disables og.storage
	Context   any               // the ctx argument passed to hooks/capabilities
	UserAgent string
	// Pure restricts og to log, encoding and crypto.sha256 (billing meters:
	// no network, storage or secrets).
	Pure bool
}

type LogLine struct {
	Level   string `json:"level"`
	Message string `json:"message"`
}

type FetchRecord struct {
	Method     string `json:"method"`
	URL        string `json:"url"`
	Status     int    `json:"status"`
	Error      string `json:"error,omitempty"`
	DurationMs int64  `json:"durationMs"`
}

type callState struct {
	ctx       context.Context
	e         *Engine
	vm        *goja.Runtime
	env       *Env
	nonce     string
	handles   map[string]string // handle -> plaintext
	logs      []LogLine
	fetches   []FetchRecord
	maxOutput int
}

func newCallState(ctx context.Context, e *Engine, vm *goja.Runtime, env *Env) *callState {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	if env == nil {
		env = &Env{}
	}
	return &callState{ctx: ctx, e: e, vm: vm, env: env, nonce: hex.EncodeToString(b), handles: map[string]string{}, maxOutput: e.cfg.MaxOutput}
}

func (cs *callState) throw(format string, a ...any) {
	panic(cs.vm.NewTypeError(fmt.Sprintf(format, a...)))
}

// bind installs the og host object (and console) for this call.
func (cs *callState) bind() {
	vm := cs.vm
	og := vm.NewObject()
	if cs.env.Pure {
		cryptoObj := vm.NewObject()
		_ = cryptoObj.Set("sha256", func(data string, enc goja.Value) string {
			sum := sha256.Sum256([]byte(data))
			return encode(sum[:], enc)
		})
		_ = og.Set("crypto", cryptoObj)
		_ = og.Set("encoding", cs.encodingObject())
		cs.bindLog(og)
		return
	}
	_ = og.Set("fetch", cs.fetch)
	_ = og.Set("secret", func(name string) string {
		v, ok := cs.env.Secrets[name]
		if !ok {
			cs.throw("密钥 %q 未在 permissions.secrets 中声明或未配置", name)
		}
		h := "{{og-secret:" + name + ":" + cs.nonce + "}}"
		cs.handles[h] = v
		return h
	})
	cryptoObj := vm.NewObject()
	_ = cryptoObj.Set("sha256", func(data string, enc goja.Value) string {
		sum := sha256.Sum256([]byte(cs.substitute(data)))
		return encode(sum[:], enc)
	})
	_ = cryptoObj.Set("hmacSha256", func(key, data string, enc goja.Value) string {
		m := hmac.New(sha256.New, []byte(cs.substitute(key)))
		m.Write([]byte(cs.substitute(data)))
		return encode(m.Sum(nil), enc)
	})
	_ = og.Set("crypto", cryptoObj)
	_ = og.Set("encoding", cs.encodingObject())
	_ = og.Set("storage", cs.storageObject())
	cs.bindLog(og)
}

func (cs *callState) encodingObject() *goja.Object {
	encObj := cs.vm.NewObject()
	_ = encObj.Set("base64Encode", func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) })
	_ = encObj.Set("base64Decode", func(s string) string {
		b, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			cs.throw("base64 解码失败")
		}
		return string(b)
	})
	return encObj
}

// bindLog installs og.log and console, then og itself.
func (cs *callState) bindLog(og *goja.Object) {
	vm := cs.vm
	logObj := vm.NewObject()
	for _, lvl := range []string{"info", "warn", "error"} {
		_ = logObj.Set(lvl, cs.logger(lvl))
	}
	_ = og.Set("log", logObj)
	_ = vm.Set("og", og)
	console := vm.NewObject()
	_ = console.Set("log", cs.logger("info"))
	_ = console.Set("warn", cs.logger("warn"))
	_ = console.Set("error", cs.logger("error"))
	_ = vm.Set("console", console)
}

func encode(b []byte, enc goja.Value) string {
	if enc != nil && !goja.IsUndefined(enc) && enc.String() == "base64" {
		return base64.StdEncoding.EncodeToString(b)
	}
	return hex.EncodeToString(b)
}

func (cs *callState) substitute(s string) string {
	for h, v := range cs.handles {
		s = strings.ReplaceAll(s, h, v)
	}
	return s
}

// redact removes handles and secret values from text that leaves the sandbox.
func (cs *callState) redact(s string) string {
	for h, v := range cs.handles {
		s = strings.ReplaceAll(s, h, "[secret]")
		if len(v) >= 6 {
			s = strings.ReplaceAll(s, v, "[secret]")
		}
	}
	for _, v := range cs.env.Secrets {
		if len(v) >= 6 {
			s = strings.ReplaceAll(s, v, "[secret]")
		}
	}
	return s
}

func (cs *callState) logger(level string) func(goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		if len(cs.logs) >= 200 {
			return goja.Undefined()
		}
		parts := make([]string, len(call.Arguments))
		for i, a := range call.Arguments {
			if o, ok := a.(*goja.Object); ok && o.ClassName() != "Error" {
				if b, err := json.Marshal(o.Export()); err == nil {
					parts[i] = string(b)
					continue
				}
			}
			parts[i] = a.String()
		}
		cs.logs = append(cs.logs, LogLine{Level: level, Message: truncate(cs.redact(strings.Join(parts, " ")), 2000)})
		return goja.Undefined()
	}
}

func (cs *callState) storageObject() *goja.Object {
	vm := cs.vm
	obj := vm.NewObject()
	need := func() {
		if cs.env.Storage == nil {
			cs.throw("插件存储在当前上下文不可用（未声明 permissions.storage，或处于不绑定渠道的模拟测试）")
		}
	}
	_ = obj.Set("get", func(key string) goja.Value {
		need()
		raw, ok, err := cs.env.Storage.Get(cs.ctx, key)
		if err != nil {
			cs.throw("存储读取失败：%v", err)
		}
		if !ok {
			return goja.Null()
		}
		v, err := cs.parseJSON(raw)
		if err != nil {
			return goja.Null()
		}
		return v
	})
	_ = obj.Set("set", func(key string, value goja.Value) {
		need()
		raw, err := cs.stringify(value)
		if err != nil {
			cs.throw("存储的值必须可以 JSON 序列化")
		}
		if err := cs.env.Storage.Set(cs.ctx, key, raw); err != nil {
			cs.throw("%v", err)
		}
	})
	_ = obj.Set("delete", func(key string) {
		need()
		if err := cs.env.Storage.Delete(cs.ctx, key); err != nil {
			cs.throw("%v", err)
		}
	})
	return obj
}

func (cs *callState) parseJSON(raw json.RawMessage) (goja.Value, error) {
	parse, _ := goja.AssertFunction(cs.vm.Get("JSON").ToObject(cs.vm).Get("parse"))
	return parse(goja.Undefined(), cs.vm.ToValue(string(raw)))
}

func (cs *callState) stringify(v goja.Value) (json.RawMessage, error) {
	stringify, _ := goja.AssertFunction(cs.vm.Get("JSON").ToObject(cs.vm).Get("stringify"))
	out, err := stringify(goja.Undefined(), v)
	if err != nil {
		return nil, err
	}
	if out == nil || goja.IsUndefined(out) {
		return json.RawMessage("null"), nil
	}
	return json.RawMessage(out.String()), nil
}

// invoke calls a plugin function with JSON args and returns its JSON result,
// awaiting a returned promise (host functions are synchronous, so promises
// are settled by the time the call returns).
func (cs *callState) invoke(ent *vmEntry, path []string, args ...any) (out json.RawMessage, err error) {
	fn, this := lookup(ent.vm, ent.plugin, path)
	if fn == nil {
		return nil, &Error{Kind: "missing", Message: "插件没有实现 " + strings.Join(path, ".")}
	}
	jsArgs := make([]goja.Value, len(args))
	for i, a := range args {
		var raw []byte
		switch v := a.(type) {
		case goja.Value:
			jsArgs[i] = v
			continue
		case json.RawMessage:
			raw = v
		default:
			if raw, err = json.Marshal(v); err != nil {
				return nil, err
			}
		}
		if jsArgs[i], err = ent.parse(goja.Undefined(), ent.vm.ToValue(string(raw))); err != nil {
			return nil, classify(ent.vm, err)
		}
	}
	v, err := fn(this, jsArgs...)
	if err != nil {
		return nil, classify(ent.vm, err)
	}
	if p, ok := v.Export().(*goja.Promise); ok {
		switch p.State() {
		case goja.PromiseStateFulfilled:
			v = p.Result()
		case goja.PromiseStateRejected:
			r := p.Result()
			msg := r.String()
			if o, ok := r.(*goja.Object); ok {
				if m := o.Get("message"); m != nil && !goja.IsUndefined(m) {
					msg = m.String()
				}
			}
			return nil, &Error{Kind: "exception", Message: cs.redact(msg)}
		default:
			return nil, &Error{Kind: "promise", Message: "插件返回的 Promise 没有完成（不支持定时器或外部异步操作）"}
		}
	}
	s, err := ent.stringify(goja.Undefined(), v)
	if err != nil {
		return nil, classify(ent.vm, err)
	}
	if s == nil || goja.IsUndefined(s) {
		return json.RawMessage("null"), nil
	}
	if len(s.String()) > cs.maxOutput {
		return nil, &Error{Kind: "output", Message: fmt.Sprintf("输出超过 %d 字节", cs.maxOutput)}
	}
	return json.RawMessage(s.String()), nil
}

// substituteHeaders replaces secret handles in a hook result's header values.
func (cs *callState) substituteHeaders(raw json.RawMessage) (json.RawMessage, error) {
	var req map[string]json.RawMessage
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, &Error{Kind: "output", Message: "Hook 必须返回请求对象"}
	}
	var headers map[string]string
	if h, ok := req["headers"]; ok && len(h) > 0 && string(h) != "null" {
		if err := json.Unmarshal(h, &headers); err != nil {
			return nil, &Error{Kind: "output", Message: "headers 必须是字符串字典"}
		}
		for k, v := range headers {
			headers[k] = cs.substitute(v)
		}
		req["headers"], _ = json.Marshal(headers)
	}
	return json.Marshal(req)
}

// ---- og.fetch ----

type fetchOpts struct {
	Method    string            `json:"method"`
	Headers   map[string]string `json:"headers"`
	Body      json.RawMessage   `json:"body"`
	TimeoutMs int               `json:"timeoutMs"`
}

func (cs *callState) fetch(call goja.FunctionCall) goja.Value {
	vm := cs.vm
	promise, resolve, reject := vm.NewPromise()
	fail := func(rec *FetchRecord, err error) goja.Value {
		rec.Error = cs.redact(err.Error())
		cs.fetches = append(cs.fetches, *rec)
		reject(vm.NewGoError(errors.New(rec.Error)))
		return vm.ToValue(promise)
	}
	rawURL := call.Argument(0).String()
	var opts fetchOpts
	if a := call.Argument(1); !goja.IsUndefined(a) && !goja.IsNull(a) {
		raw, err := cs.stringify(a)
		if err == nil {
			err = json.Unmarshal(raw, &opts)
		}
		if err != nil {
			return fail(&FetchRecord{URL: cs.redact(rawURL)}, errors.New("fetch 选项格式错误"))
		}
	}
	method := strings.ToUpper(opts.Method)
	if method == "" {
		method = http.MethodGet
	}
	rec := &FetchRecord{Method: method, URL: cs.redact(rawURL)}
	if len(cs.fetches) >= cs.e.cfg.MaxFetches {
		return fail(rec, fmt.Errorf("单次调用最多 %d 次 fetch", cs.e.cfg.MaxFetches))
	}
	u, err := url.Parse(cs.substitute(rawURL))
	if err != nil || u.Host == "" {
		return fail(rec, errors.New("URL 无效"))
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && cs.env.AllowHTTP) {
		return fail(rec, errors.New("只允许 https"))
	}
	if !hostAllowed(u.Hostname(), cs.env.Network, cs.env.BaseHost) {
		return fail(rec, fmt.Errorf("主机 %s 未在 permissions.network 中声明", u.Hostname()))
	}
	var body []byte
	if len(opts.Body) > 0 && string(opts.Body) != "null" {
		var s string
		if json.Unmarshal(opts.Body, &s) == nil {
			body = []byte(cs.substitute(s))
		} else {
			body = []byte(cs.substitute(string(opts.Body)))
			if opts.Headers == nil {
				opts.Headers = map[string]string{}
			}
			if _, ok := opts.Headers["Content-Type"]; !ok {
				opts.Headers["Content-Type"] = "application/json"
			}
		}
	}
	start := time.Now()
	status, respHeaders, respBody, err := cs.do(method, u, opts, body)
	rec.DurationMs = time.Since(start).Milliseconds()
	rec.Status = status
	if err != nil {
		return fail(rec, err)
	}
	cs.fetches = append(cs.fetches, *rec)
	res := vm.NewObject()
	_ = res.Set("status", status)
	_ = res.Set("ok", status >= 200 && status < 300)
	_ = res.Set("headers", respHeaders)
	text := string(respBody)
	_ = res.Set("text", func() string { return text })
	_ = res.Set("json", func() goja.Value {
		v, err := cs.parseJSON(json.RawMessage(text))
		if err != nil {
			cs.throw("响应不是合法的 JSON")
		}
		return v
	})
	resolve(res)
	return vm.ToValue(promise)
}

func (cs *callState) do(method string, u *url.URL, opts fetchOpts, body []byte) (int, map[string]string, []byte, error) {
	if cs.env.Mocks != nil {
		target := u.String()
		for _, m := range cs.env.Mocks {
			mm := strings.ToUpper(m.Match.Method)
			if (mm == "" || mm == method) && m.Match.URL == target {
				st := m.Response.Status
				if st == 0 {
					st = 200
				}
				b := []byte(m.Response.Body)
				if len(m.Response.JSON) > 0 {
					b = m.Response.JSON
				}
				h := map[string]string{}
				for k, v := range m.Response.Headers {
					h[strings.ToLower(k)] = v
				}
				if _, ok := h["content-type"]; !ok && len(m.Response.JSON) > 0 {
					h["content-type"] = "application/json"
				}
				return st, h, b, nil
			}
		}
		return 0, nil, nil, fmt.Errorf("模拟测试中没有匹配 %s %s 的 fetch 响应", method, cs.redact(target))
	}
	if cs.env.HTTP == nil {
		return 0, nil, nil, errors.New("此上下文不允许网络访问")
	}
	timeout := 10 * time.Second
	if opts.TimeoutMs > 0 {
		timeout = min(time.Duration(opts.TimeoutMs)*time.Millisecond, 30*time.Second)
	}
	ctx, cancel := context.WithTimeout(cs.ctx, timeout)
	defer cancel()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return 0, nil, nil, err
	}
	keys := make([]string, 0, len(opts.Headers))
	for k := range opts.Headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		req.Header.Set(k, cs.substitute(opts.Headers[k]))
	}
	if cs.env.UserAgent != "" && req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", cs.env.UserAgent)
	}
	resp, err := cs.env.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, nil, nil, errors.New("请求超时")
		}
		return 0, nil, nil, errors.New("请求失败：" + err.Error())
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, cs.e.cfg.MaxFetchBody+1))
	if err != nil {
		return resp.StatusCode, nil, nil, errors.New("读取响应失败")
	}
	if int64(len(b)) > cs.e.cfg.MaxFetchBody {
		return resp.StatusCode, nil, nil, fmt.Errorf("响应超过 %d 字节", cs.e.cfg.MaxFetchBody)
	}
	h := map[string]string{}
	for k, v := range resp.Header {
		h[strings.ToLower(k)] = strings.Join(v, ", ")
	}
	return resp.StatusCode, h, b, nil
}

func hostAllowed(host string, network []string, baseHost string) bool {
	host = strings.ToLower(host)
	for _, n := range network {
		switch {
		case n == "$baseUrl":
			if baseHost != "" && host == strings.ToLower(baseHost) {
				return true
			}
		case strings.HasPrefix(n, "*."):
			if strings.HasSuffix(host, n[1:]) && host != n[2:] {
				return true
			}
		case host == n:
			return true
		}
	}
	return false
}
