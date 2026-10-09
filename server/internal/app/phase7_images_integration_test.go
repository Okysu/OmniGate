package app_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Round 6 (continued), docs/contracts/phase7-api.md §1: OpenAI image endpoints.

type imgFile struct {
	Name, Type string
	Data       []byte
}

// imgReq is what the fake image upstream received.
type imgReq struct {
	Path, ContentType, Auth string
	Header                  http.Header
	ContentLength           int64
	Fields                  map[string][]string
	Files                   map[string][]imgFile
	JSON                    map[string]any
}

// imgUpstream is an OpenAI-compatible image API: data has n items; usage is
// reported unless the upstream model contains "dall-e" or the endpoint is
// variations; stream requests get partial + completed events.
type imgUpstream struct {
	srv  *httptest.Server
	fail atomic.Bool // answer 500 (after reading the whole request)
	hits atomic.Int64
	mu   sync.Mutex
	reqs []imgReq
}

const imgUsage = `{"input_tokens":50,"output_tokens":1000,"total_tokens":1050,"input_tokens_details":{"text_tokens":20,"image_tokens":30}}`

func newImgUpstream(t *testing.T) *imgUpstream {
	u := &imgUpstream{}
	u.srv = httptest.NewServer(http.HandlerFunc(u.serve))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *imgUpstream) last() imgReq {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.reqs[len(u.reqs)-1]
}

func (u *imgUpstream) serve(w http.ResponseWriter, r *http.Request) {
	u.hits.Add(1)
	rec := imgReq{Path: r.URL.Path, ContentType: r.Header.Get("Content-Type"), Auth: r.Header.Get("Authorization"),
		Header: r.Header.Clone(), ContentLength: r.ContentLength}
	var model string
	n, stream := 1, false
	if strings.HasPrefix(rec.ContentType, "multipart/form-data") {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			http.Error(w, `{"error":{"message":"bad multipart: `+err.Error()+`"}}`, http.StatusBadRequest)
			return
		}
		rec.Fields, rec.Files = r.MultipartForm.Value, map[string][]imgFile{}
		for name, fhs := range r.MultipartForm.File {
			for _, fh := range fhs {
				f, _ := fh.Open()
				b, _ := io.ReadAll(f)
				f.Close()
				rec.Files[name] = append(rec.Files[name], imgFile{fh.Filename, fh.Header.Get("Content-Type"), b})
			}
		}
		model = r.FormValue("model")
		if v, err := strconv.Atoi(r.FormValue("n")); err == nil {
			n = v
		}
		stream = r.FormValue("stream") == "true"
	} else {
		_ = json.NewDecoder(r.Body).Decode(&rec.JSON)
		model, _ = rec.JSON["model"].(string)
		if v, ok := rec.JSON["n"].(float64); ok {
			n = int(v)
		}
		stream, _ = rec.JSON["stream"].(bool)
	}
	u.mu.Lock()
	u.reqs = append(u.reqs, rec)
	u.mu.Unlock()
	if u.fail.Load() {
		http.Error(w, `{"error":{"message":"image backend down"}}`, http.StatusInternalServerError)
		return
	}
	withUsage := !strings.Contains(model, "dall-e") && !strings.HasSuffix(r.URL.Path, "/variations")
	prefix := "image_generation"
	if strings.HasSuffix(r.URL.Path, "/edits") {
		prefix = "image_edit"
	}
	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
		ev := func(typ, data string) {
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", typ, data)
			w.(http.Flusher).Flush()
		}
		ev(prefix+".partial_image", `{"type":"`+prefix+`.partial_image","b64_json":"cGFydA==","partial_image_index":0}`)
		for i := 0; i < n; i++ {
			usage := ""
			if withUsage {
				usage = `,"usage":` + imgUsage
			}
			ev(prefix+".completed", `{"type":"`+prefix+`.completed","b64_json":"aW1n"`+usage+`}`)
		}
		return
	}
	data := make([]string, n)
	for i := range data {
		data[i] = `{"b64_json":"aW1n"}`
	}
	usage := ""
	if withUsage {
		usage = `,"usage":` + imgUsage
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"created":1,"data":[%s]%s}`, strings.Join(data, ","), usage)
}

// mpPart is one multipart field (filename set = file part).
type mpPart struct {
	name, filename, ctype string
	data                  []byte
}

func mpBody(parts ...mpPart) (string, []byte) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, p := range parts {
		h := textproto.MIMEHeader{}
		if p.filename != "" {
			h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, p.name, p.filename))
			h.Set("Content-Type", p.ctype)
		} else {
			h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q`, p.name))
		}
		w, _ := mw.CreatePart(h)
		_, _ = w.Write(p.data)
	}
	_ = mw.Close()
	return mw.FormDataContentType(), buf.Bytes()
}

func field(name, value string) mpPart { return mpPart{name: name, data: []byte(value)} }

func file(name, filename string, data []byte) mpPart {
	return mpPart{name: name, filename: filename, ctype: "image/png", data: data}
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func gwPostCT(t *testing.T, base, path, key, ct string, body io.Reader) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, base+path, body)
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", ct)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func imgModels(pairs ...string) []map[string]string {
	var out []map[string]string
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, map[string]string{"model": pairs[i], "upstreamModel": pairs[i+1]})
	}
	return out
}

func TestImageEndpoints(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir()) // spooled multipart bodies land here
	e := setupGateway(t)
	base := e.h.srv.URL
	up := newImgUpstream(t)
	e.platformChannel(map[string]any{"name": "img", "type": "openai", "baseUrl": up.srv.URL + "/v1",
		"models": imgModels("img", "up-img", "dall-e-3", "dall-e-3", "dall-e-2", "dall-e-2", "img-q", "up-img-q")})
	e.mustDo(e.admin, http.MethodPost, "/api/admin/prices", map[string]any{"kind": "sell", "model": "img", "inputPerM": "10", "outputPerM": "40",
		"perRequest": "0.001", "perImage": "0.01", "imageInputPerM": "20"}, 201)
	p := e.mustDo(e.admin, http.MethodPost, "/api/admin/prices", map[string]any{"kind": "sell", "model": "dall-e-3", "perImage": "0.04", "imageInputPerM": nil}, 201)
	if p["perImage"] != "0.04" || p["imageInputPerM"] != nil || p["inputPerM"] != "0" {
		t.Fatalf("price = %v", p)
	}
	e.mustDo(e.admin, http.MethodPost, "/api/admin/prices", map[string]any{"kind": "sell", "model": "dall-e-2", "perImage": "0.02"}, 201)
	_, key := e.key(e.admin, map[string]any{"name": "img"})

	t.Run("generations: usage-based billing with n>1", func(t *testing.T) {
		code, body, raw := readBody(gwPost(t, context.Background(), base, "/v1/images/generations", key, `{"model":"img","prompt":"a cat","n":2,"size":"1024x1024"}`))
		if code != 200 || len(body["data"].([]any)) != 2 || body["usage"] == nil {
			t.Fatalf("generations = %d %s", code, raw)
		}
		if got := up.last(); got.Path != "/v1/images/generations" || got.JSON["model"] != "up-img" || got.JSON["size"] != "1024x1024" || got.JSON["n"] != float64(2) {
			t.Fatalf("upstream got %+v", got)
		}
		l := e.lastLog(e.admin, "model=img")
		usage := l["usage"].(map[string]any)
		// 0.001 + 20×10/1M + 30×20/1M + 1000×40/1M + 2×0.01
		if l["inbound"] != "openai.images.generations" || l["imageCount"] != float64(2) || usage["input"] != float64(50) ||
			usage["imageInputTokens"] != float64(30) || usage["output"] != float64(1000) || l["charge"] != "0.0618" || l["stream"] != false {
			t.Fatalf("log = %v", l)
		}
	})

	t.Run("generations: per-image billing without usage", func(t *testing.T) {
		code, body, raw := readBody(gwPost(t, context.Background(), base, "/v1/images/generations", key, `{"model":"dall-e-3","prompt":"a dog"}`))
		if code != 200 || len(body["data"].([]any)) != 1 || body["usage"] != nil {
			t.Fatalf("dall-e-3 = %d %s", code, raw)
		}
		l := e.lastLog(e.admin, "model=dall-e-3")
		usage := l["usage"].(map[string]any)
		if l["imageCount"] != float64(1) || usage["input"] != float64(0) || usage["output"] != float64(0) || usage["estimated"] != false ||
			l["charge"] != "0.04" {
			t.Fatalf("log = %v", l)
		}
	})

	t.Run("edits: multipart with two images and a mask is re-encoded intact", func(t *testing.T) {
		img1, img2, mask := randomBytes(300<<10), randomBytes(200<<10), randomBytes(50<<10)
		ct, body := mpBody(field("prompt", "make it blue"), file("image[]", "a.png", img1), field("model", "img"),
			file("image[]", "b.png", img2), file("mask", "mask.png", mask), field("n", "1"), field("quality", "high"))
		code, out, raw := readBody(gwPostCT(t, base, "/v1/images/edits", key, ct, bytes.NewReader(body)))
		if code != 200 || len(out["data"].([]any)) != 1 {
			t.Fatalf("edits = %d %s", code, raw)
		}
		got := up.last()
		if got.Path != "/v1/images/edits" || !strings.HasPrefix(got.ContentType, "multipart/form-data; boundary=") || got.ContentLength <= 0 {
			t.Fatalf("upstream request: path=%s ct=%s len=%d", got.Path, got.ContentType, got.ContentLength)
		}
		if got.Fields["model"][0] != "up-img" || got.Fields["prompt"][0] != "make it blue" || got.Fields["quality"][0] != "high" || got.Fields["n"][0] != "1" {
			t.Fatalf("fields = %v", got.Fields)
		}
		imgs := got.Files["image[]"]
		if len(imgs) != 2 || !bytes.Equal(imgs[0].Data, img1) || !bytes.Equal(imgs[1].Data, img2) || imgs[0].Name != "a.png" || imgs[1].Type != "image/png" {
			t.Fatalf("images = %d files", len(imgs))
		}
		if m := got.Files["mask"]; len(m) != 1 || !bytes.Equal(m[0].Data, mask) || m[0].Name != "mask.png" {
			t.Fatal("mask not forwarded intact")
		}
		if got.Auth != "Bearer sk-upstream-secret-0123456789" {
			t.Fatalf("auth = %q", got.Auth)
		}
		l := e.lastLog(e.admin, "model=img")
		if l["inbound"] != "openai.images.edits" || l["imageCount"] != float64(1) || l["charge"] != "0.0518" {
			t.Fatalf("log = %v", l)
		}
	})

	t.Run("edits: a body above the in-memory threshold is spooled and removed", func(t *testing.T) {
		big := randomBytes(9 << 20)
		ct, body := mpBody(field("model", "img"), field("prompt", "big"), file("image", "big.png", big))
		code, _, raw := readBody(gwPostCT(t, base, "/v1/images/edits", key, ct, bytes.NewReader(body)))
		if code != 200 {
			t.Fatalf("big edit = %d %s", code, raw)
		}
		if f := up.last().Files["image"]; len(f) != 1 || !bytes.Equal(f[0].Data, big) {
			t.Fatal("large image not forwarded intact")
		}
		left, _ := filepath.Glob(filepath.Join(os.TempDir(), "omnigate-image-*"))
		if len(left) != 0 {
			t.Fatalf("temporary files left: %v", left)
		}
	})

	t.Run("variations", func(t *testing.T) {
		ct, body := mpBody(file("image", "v.png", randomBytes(1000)), field("model", "dall-e-2"), field("n", "3"))
		code, out, raw := readBody(gwPostCT(t, base, "/v1/images/variations", key, ct, bytes.NewReader(body)))
		if code != 200 || len(out["data"].([]any)) != 3 {
			t.Fatalf("variations = %d %s", code, raw)
		}
		if got := up.last(); got.Path != "/v1/images/variations" || got.Fields["model"][0] != "dall-e-2" || len(got.Files["image"]) != 1 {
			t.Fatalf("upstream got %+v", got)
		}
		l := e.lastLog(e.admin, "model=dall-e-2")
		if l["inbound"] != "openai.images.variations" || l["imageCount"] != float64(3) || l["charge"] != "0.06" {
			t.Fatalf("log = %v", l)
		}
	})

	t.Run("stream: SSE passthrough with usage from the completed event", func(t *testing.T) {
		resp := gwPost(t, context.Background(), base, "/v1/images/generations", key, `{"model":"img","prompt":"p","stream":true,"partial_images":1}`)
		code, _, raw := readBody(resp)
		if code != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") ||
			!strings.Contains(raw, "event: image_generation.partial_image") || !strings.Contains(raw, `"type":"image_generation.completed"`) {
			t.Fatalf("stream = %d %q", code, raw)
		}
		l := e.lastLog(e.admin, "model=img")
		usage := l["usage"].(map[string]any)
		if l["stream"] != true || l["imageCount"] != float64(1) || usage["input"] != float64(50) || usage["imageInputTokens"] != float64(30) || l["charge"] != "0.0518" {
			t.Fatalf("log = %v", l)
		}
		// Streamed edits (multipart, stream=true).
		ct, body := mpBody(field("model", "img"), field("stream", "true"), file("image", "s.png", randomBytes(100)))
		resp = gwPostCT(t, base, "/v1/images/edits", key, ct, bytes.NewReader(body))
		if code, _, raw := readBody(resp); code != 200 || !strings.Contains(raw, "image_edit.completed") {
			t.Fatalf("streamed edit = %d %q", code, raw)
		}
	})

	t.Run("body limit: 413 before contacting the upstream", func(t *testing.T) {
		before := up.hits.Load()
		ct, body := mpBody(field("model", "img"), file("image", "huge.png", make([]byte, 64<<20)))
		code, out, _ := readBody(gwPostCT(t, base, "/v1/images/edits", key, ct, bytes.NewReader(body)))
		if code != http.StatusRequestEntityTooLarge || out["error"] == nil {
			t.Fatalf("oversized = %d %v", code, out)
		}
		if up.hits.Load() != before {
			t.Fatal("oversized request reached the upstream")
		}
	})

	t.Run("images meter in plan quotas", func(t *testing.T) {
		plan := e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/plans", map[string]any{
			"name": "画图套餐", "description": "", "duration": "30d", "models": []string{"img-q"}, "stackable": false,
			"rules": []map[string]any{{"id": "pics", "label": "每日图片", "meter": "images", "window": map[string]any{"kind": "calendar", "unit": "day"},
				"limit": "3", "modelWeights": map[string]string{"img-q": "1"}}},
		}, 201)
		e.mustDo(e.admin, http.MethodPost, "/api/admin/billing/subscriptions", map[string]any{"userId": e.userID(e.admin), "planId": plan["id"], "periods": 1}, 201)
		for i := 0; i < 2; i++ {
			if code, _, raw := readBody(gwPost(t, context.Background(), base, "/v1/images/generations", key, `{"model":"img-q","prompt":"p","n":2}`)); code != 200 {
				t.Fatalf("covered image request %d = %d %s", i, code, raw)
			}
			e.app.FlushLogs(context.Background())
		}
		code, body, raw := readBody(gwPost(t, context.Background(), base, "/v1/images/generations", key, `{"model":"img-q","prompt":"p"}`))
		if code != 429 || body["error"].(map[string]any)["code"] != "quota_exceeded" {
			t.Fatalf("over image quota = %d %s", code, raw)
		}
		subs := e.mustDo(e.admin, http.MethodGet, "/api/billing/subscriptions", nil, 200)
		rule := subs["items"].([]any)[0].(map[string]any)["rules"].([]any)[0].(map[string]any)
		if rule["meter"] != "images" || rule["used"] != "4" || rule["exceeded"] != true {
			t.Fatalf("rule usage = %v", rule)
		}
		l := e.lastLog(e.admin, "model=img-q&status=success")
		if l["subscriptionId"] == nil || l["imageCount"] != float64(2) || l["charge"] != "0" {
			t.Fatalf("covered log = %v", l)
		}
	})

	t.Run("prepaid: perRequest + n × perImage is reserved", func(t *testing.T) {
		e.platformChannel(map[string]any{"name": "img-glob", "type": "openai", "scope": "global", "baseUrl": up.srv.URL + "/v1", "models": imgModels("img-b", "dall-e-b")})
		e.mustDo(e.admin, http.MethodPost, "/api/admin/prices", map[string]any{"kind": "sell", "model": "img-b", "perImage": "1"}, 201)
		e.enforceBilling()
		_, carolKey := e.key(e.carol, map[string]any{"name": "c"})
		code, body, raw := readBody(gwPost(t, context.Background(), base, "/v1/images/generations", carolKey, `{"model":"img-b","prompt":"p","n":2}`))
		if code != 402 || body["error"].(map[string]any)["code"] != "insufficient_balance" {
			t.Fatalf("no balance = %d %s", code, raw)
		}
		carolID := e.userID(e.carol)
		e.credit(carolID, "5")
		if code, _, raw := readBody(gwPost(t, context.Background(), base, "/v1/images/generations", carolKey, `{"model":"img-b","prompt":"p","n":2}`)); code != 200 {
			t.Fatalf("with balance = %d %s", code, raw)
		}
		if l := e.lastLog(e.carol, "model=img-b"); l["charge"] != "2" || l["imageCount"] != float64(2) {
			t.Fatalf("log = %v", l)
		}
		w := e.mustDo(e.carol, http.MethodGet, "/api/billing/wallet", nil, 200)
		if w["balance"] != "3" || w["reserved"] != "0" {
			t.Fatalf("wallet = %v", w)
		}
	})

	t.Run("plaza: imageGeneration capability adds the openai.images protocol and image prices", func(t *testing.T) {
		e.mustDo(e.admin, http.MethodPut, "/api/admin/model-info/img", map[string]any{"capabilities": map[string]any{"imageGeneration": true}}, 201)
		mine := e.mustDo(e.admin, http.MethodGet, "/api/plaza/mine", nil, 200)
		var m map[string]any
		for _, it := range mine["items"].([]any) {
			if it.(map[string]any)["model"] == "img" {
				m = it.(map[string]any)
			}
		}
		if m == nil || !strings.Contains(fmt.Sprint(m["protocols"]), "openai.images") || m["capabilities"].(map[string]any)["imageGeneration"] != true {
			t.Fatalf("plaza entry = %v", m)
		}
		price := m["price"].(map[string]any)
		if price["perImage"] != "0.01" || price["imageInputPerM"] != "20" {
			t.Fatalf("plaza price = %v", price)
		}
	})
}

func TestImageRoutingRetryAndTiers(t *testing.T) {
	e := setupGateway(t)
	base := e.h.srv.URL
	bad, good := newImgUpstream(t), newImgUpstream(t)
	bad.fail.Store(true)
	e.platformChannel(map[string]any{"name": "bad", "type": "openai", "baseUrl": bad.srv.URL + "/v1", "priority": 10, "models": imgModels("img-r", "bad-img")})
	e.platformChannel(map[string]any{"name": "good", "type": "openai", "baseUrl": good.srv.URL + "/v1", "models": imgModels("img-r", "good-img")})
	_, key := e.key(e.admin, map[string]any{"name": "k"})

	t.Run("retry on another channel after 5xx replays the multipart body", func(t *testing.T) {
		img := randomBytes(256 << 10)
		ct, body := mpBody(field("model", "img-r"), field("prompt", "retry me"), file("image", "r.png", img))
		code, _, raw := readBody(gwPostCT(t, base, "/v1/images/edits", key, ct, bytes.NewReader(body)))
		if code != 200 {
			t.Fatalf("edit with retry = %d %s", code, raw)
		}
		b, g := bad.last(), good.last()
		if b.Fields["model"][0] != "bad-img" || g.Fields["model"][0] != "good-img" {
			t.Fatalf("models: bad=%v good=%v", b.Fields["model"], g.Fields["model"])
		}
		for _, r := range []imgReq{b, g} {
			if f := r.Files["image"]; len(f) != 1 || !bytes.Equal(f[0].Data, img) || r.Fields["prompt"][0] != "retry me" {
				t.Fatal("replayed body differs")
			}
		}
		l := e.lastLog(e.admin, "model=img-r")
		if l["attempts"] != float64(2) || l["statusCode"] != float64(200) {
			t.Fatalf("log = %v", l)
		}
	})

	t.Run("anthropic-only models are not available on image endpoints", func(t *testing.T) {
		e.channel(e.admin, map[string]any{"name": "claude", "type": "anthropic", "baseUrl": good.srv.URL, "models": models("img-a")})
		code, body, raw := readBody(gwPost(t, context.Background(), base, "/v1/images/generations", key, `{"model":"img-a","prompt":"p"}`))
		if code != 404 || body["error"].(map[string]any)["code"] != "model_not_found" || !strings.Contains(raw, "OpenAI-compatible") {
			t.Fatalf("anthropic-only = %d %s", code, raw)
		}
	})

	t.Run("own channels serve images for free", func(t *testing.T) {
		e.channel(e.admin, map[string]any{"name": "mine", "type": "openai", "baseUrl": good.srv.URL + "/v1", "models": imgModels("img-own", "up-own")})
		e.mustDo(e.admin, http.MethodPost, "/api/admin/prices", map[string]any{"kind": "sell", "model": "img-own", "perImage": "1"}, 201)
		if code, _, raw := readBody(gwPost(t, context.Background(), base, "/v1/images/generations", key, `{"model":"img-own","prompt":"p","n":2}`)); code != 200 {
			t.Fatalf("own = %d %s", code, raw)
		}
		l := e.lastLog(e.admin, "model=img-own")
		if l["channelTier"] != "own" || l["charge"] != "0" || l["imageCount"] != float64(2) {
			t.Fatalf("own log = %v", l)
		}
	})

	t.Run("plugin hooks: transformRequest only for JSON, signRequest for multipart too", func(t *testing.T) {
		pl := e.mustDo(e.admin, http.MethodPost, "/api/plugins", map[string]any{"id": "acme.signer", "name": "签名", "template": "blank"}, 201)
		pid := pl["id"].(string)
		e.mustDo(e.admin, http.MethodPut, "/api/plugins/"+pid+"/draft", map[string]any{"files": signerFiles("0.1.0", ""), "version": 1}, 200)
		v := e.mustDo(e.admin, http.MethodPost, "/api/plugins/"+pid+"/draft/publish", nil, 201)
		e.mustDo(e.admin, http.MethodPost, "/api/plugins/"+pid+"/versions/"+v["id"].(string)+"/approve", map[string]any{"decision": "approve"}, 200)
		e.mustDo(e.admin, http.MethodPost, "/api/channels", map[string]any{"name": "signed", "pluginVersionId": v["id"], "baseUrl": good.srv.URL,
			"apiKey": "sk-custom-999", "models": imgModels("img-p", "up-p"), "pluginConfig": map[string]any{"tenant": "t-1"}}, 201)
		time.Sleep(80 * time.Millisecond)
		if code, _, raw := readBody(gwPost(t, context.Background(), base, "/v1/images/generations", key, `{"model":"img-p","prompt":"p"}`)); code != 200 {
			t.Fatalf("json via plugin = %d %s", code, raw)
		}
		j := good.last()
		if j.Path != "/custom/images/generations" || j.JSON["tenant"] != "t-1" || j.Header.Get("X-Custom-Key") != "sk-custom-999" || j.Auth != "" {
			t.Fatalf("json hooks: path=%s body=%v", j.Path, j.JSON)
		}
		ct, body := mpBody(field("model", "img-p"), file("image", "p.png", randomBytes(64)))
		if code, _, raw := readBody(gwPostCT(t, base, "/v1/images/edits", key, ct, bytes.NewReader(body))); code != 200 {
			t.Fatalf("multipart via plugin = %d %s", code, raw)
		}
		m := good.last()
		if m.Path != "/images/edits" || m.Header.Get("X-Custom-Key") != "sk-custom-999" || m.Auth != "" || m.Fields["model"][0] != "up-p" || m.Fields["tenant"] != nil {
			t.Fatalf("multipart hooks: path=%s fields=%v", m.Path, m.Fields)
		}
	})
}
