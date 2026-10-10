package affinity

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"omnigate/internal/protocol"
)

func TestAnchorChat(t *testing.T) {
	first := `{"model":"gpt-6","messages":[{"role":"system","content":"You are terse."},{"role":"developer","content":[{"type":"text","text":"Answer in English."}]},
		{"role":"user","content":"Plan a trip to Rome"}]}`
	later := `{"model":"gpt-6","stream":true,"messages":[{"role":"system","content":"You are terse."},{"role":"developer","content":[{"type":"text","text":"Answer in English."}]},
		{"role":"user","content":"Plan a trip to Rome"},{"role":"assistant","content":"Day 1: …"},{"role":"user","content":"Add a day in Florence"}]}`
	a := Anchor(protocol.OpenAIChat, []byte(first))
	if len(a) != 64 {
		t.Fatalf("anchor = %q", a)
	}
	// Later turns of the same conversation keep the anchor.
	if b := Anchor(protocol.OpenAIChat, []byte(later)); b != a {
		t.Fatal("anchor changed across turns")
	}
	// A text part and a string content are the same message.
	if b := Anchor(protocol.OpenAIChat, []byte(strings.Replace(first, `"content":"Plan a trip to Rome"`, `"content":[{"type":"text","text":"Plan a trip to Rome"}]`, 1))); b != a {
		t.Fatal("string and text-part content differ")
	}
	for name, body := range map[string]string{
		"other first user": strings.Replace(later, "Plan a trip to Rome", "Plan a trip to Paris", 1),
		"other system":     strings.Replace(first, "You are terse.", "You are verbose.", 1),
		"no system":        `{"model":"gpt-6","messages":[{"role":"user","content":"Plan a trip to Rome"}]}`,
		"role boundary":    `{"model":"gpt-6","messages":[{"role":"system","content":"You are terse.Answer in English."},{"role":"user","content":"Plan a trip to Rome"}]}`,
		"image":            `{"model":"gpt-6","messages":[{"role":"user","content":[{"type":"text","text":"Plan a trip to Rome"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]}`,
	} {
		if Anchor(protocol.OpenAIChat, []byte(body)) == a {
			t.Errorf("%s: same anchor", name)
		}
	}
	// Images differ by their payload; system messages after an assistant turn
	// are not leading instructions.
	img := func(data string) string {
		return `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + data + `"}}]}]}`
	}
	if Anchor(protocol.OpenAIChat, []byte(img("AAAA"))) == Anchor(protocol.OpenAIChat, []byte(img("BBBB"))) {
		t.Error("different images, same anchor")
	}
	if Anchor(protocol.OpenAIChat, []byte(`{"messages":[{"role":"assistant","content":"hi"},{"role":"system","content":"x"},{"role":"user","content":"q"}]}`)) !=
		Anchor(protocol.OpenAIChat, []byte(`{"messages":[{"role":"assistant","content":"hello"},{"role":"user","content":"q"}]}`)) {
		t.Error("non-leading messages must not count")
	}
	// No user message, no body, other protocols: nothing.
	for name, tc := range map[string]struct {
		dialect string
		body    string
	}{
		"system only": {protocol.OpenAIChat, `{"messages":[{"role":"system","content":"x"}]}`},
		"no messages": {protocol.OpenAIChat, `{"model":"gpt-6"}`},
		"multipart":   {protocol.OpenAIChat, ``},
		"embeddings":  {protocol.OpenAIEmbeddings, `{"input":"x","messages":[{"role":"user","content":"x"}]}`},
		// FIM prompts change with every keystroke: never an anchor.
		"completions": {protocol.OpenAICompletions, `{"prompt":"def f():","suffix":"\n","messages":[{"role":"user","content":"x"}]}`},
		"not json":    {protocol.OpenAIChat, `{"messages":`},
	} {
		if v := Anchor(tc.dialect, []byte(tc.body)); v != "" {
			t.Errorf("%s: anchor = %q", name, v)
		}
	}
}

func TestAnchorResponses(t *testing.T) {
	str := `{"model":"gpt-6","instructions":"Be brief.","input":"Plan a trip to Rome"}`
	arr := `{"model":"gpt-6","instructions":"Be brief.","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Plan a trip to Rome"}]}]}`
	turn := `{"model":"gpt-6","instructions":"Be brief.","previous_response_id":"resp_9","input":[{"role":"user","content":"Plan a trip to Rome"},
		{"type":"function_call","call_id":"c1","name":"search","arguments":"{}"},{"type":"function_call_output","call_id":"c1","output":"ok"},
		{"role":"assistant","content":[{"type":"output_text","text":"Day 1"}]},{"role":"user","content":"more"}]}`
	a := Anchor(protocol.OpenAIResponses, []byte(str))
	if a == "" || Anchor(protocol.OpenAIResponses, []byte(arr)) != a || Anchor(protocol.OpenAIResponses, []byte(turn)) != a {
		t.Fatal("string input, array input and a later turn must share the anchor")
	}
	for name, body := range map[string]string{
		"instructions": `{"instructions":"Be verbose.","input":"Plan a trip to Rome"}`,
		"none":         `{"input":"Plan a trip to Rome"}`,
		"developer":    `{"instructions":"Be brief.","input":[{"role":"developer","content":"x"},{"role":"user","content":"Plan a trip to Rome"}]}`,
		"other user":   `{"instructions":"Be brief.","input":"Plan a trip to Paris"}`,
		"file part":    `{"instructions":"Be brief.","input":[{"role":"user","content":[{"type":"input_text","text":"Plan a trip to Rome"},{"type":"input_file","file_id":"file-1"}]}]}`,
	} {
		if Anchor(protocol.OpenAIResponses, []byte(body)) == a {
			t.Errorf("%s: same anchor", name)
		}
	}
	if v := Anchor(protocol.OpenAIResponses, []byte(`{"instructions":"x","input":[{"type":"item_reference","id":"msg_1"}]}`)); v != "" {
		t.Errorf("no user item: %q", v)
	}
}

func TestAnchorAnthropic(t *testing.T) {
	str := `{"model":"claude-x","max_tokens":10,"system":"Be brief.","messages":[{"role":"user","content":"Plan a trip to Rome"}]}`
	blocks := `{"model":"claude-x","max_tokens":10,"system":[{"type":"text","text":"Be brief.","cache_control":{"type":"ephemeral"}}],
		"messages":[{"role":"user","content":[{"type":"text","text":"Plan a trip to Rome","cache_control":{"type":"ephemeral"}}]},
		{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"x","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}]}`
	a := Anchor(protocol.Anthropic, []byte(str))
	if a == "" || Anchor(protocol.Anthropic, []byte(blocks)) != a {
		t.Fatal("string and block system / content must share the anchor; cache_control is ignored")
	}
	// cache_control moving off a non-text block does not change the anchor.
	img := `{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}%s}]}]}`
	if Anchor(protocol.Anthropic, []byte(strings.Replace(img, "%s", `,"cache_control":{"type":"ephemeral"}`, 1))) != Anchor(protocol.Anthropic, []byte(strings.Replace(img, "%s", "", 1))) {
		t.Error("cache_control changed the anchor")
	}
	if Anchor(protocol.Anthropic, []byte(`{"system":"Be brief.","messages":[{"role":"user","content":"Plan a trip to Paris"}]}`)) == a {
		t.Error("other first user message, same anchor")
	}
	if v := Anchor(protocol.Anthropic, []byte(`{"system":"Be brief.","messages":[]}`)); v != "" {
		t.Errorf("no user message: %q", v)
	}
}

func TestAnchorBounded(t *testing.T) {
	huge := strings.Repeat("x", 8<<20)
	body := func(tail string) []byte {
		return []byte(`{"messages":[{"role":"system","content":"` + huge + tail + `"},{"role":"user","content":"q"}]}`)
	}
	start := time.Now()
	a := Anchor(protocol.OpenAIChat, body("a"))
	if a == "" || time.Since(start) > 2*time.Second {
		t.Fatalf("huge body: %q in %s", a, time.Since(start))
	}
	// Units are bounded to their first 4 KiB plus the full length: a
	// difference past the bound with the same length is not seen, a
	// different length is.
	if Anchor(protocol.OpenAIChat, body("b")) != a {
		t.Error("unit not bounded")
	}
	if Anchor(protocol.OpenAIChat, body("bb")) == a {
		t.Error("unit length not hashed")
	}
}

func TestAnchorKeySource(t *testing.T) {
	c := mustDecode(t, `{"rules":[
	  {"name":"hdr","model_regex":["^gpt-"],"key_sources":[{"type":"request_header","key":"Session_id"}]},
	  {"name":"gpt","model_regex":["^gpt-"],"key_sources":[{"type":"gjson","path":"user"},{"type":"anchor"}]}
	]}`)
	body := []byte(`{"model":"gpt-6","messages":[{"role":"user","content":"hi"}]}`)
	req := Request{Model: "gpt-6", Path: "/v1/chat/completions", Dialect: protocol.OpenAIChat, Header: http.Header{}, Body: body}
	m := c.Match(req)
	if m == nil || m.Rule.Name != "gpt" || m.Value != Anchor(protocol.OpenAIChat, body) {
		t.Fatalf("match = %+v", m)
	}
	// Earlier sources win over the anchor.
	req.Body = []byte(`{"model":"gpt-6","user":"end-user-1","messages":[{"role":"user","content":"hi"}]}`)
	if m := c.Match(req); m == nil || m.Value != "end-user-1" {
		t.Fatalf("user source = %+v", m)
	}
	// Multipart (no body) and a body without a user message: nothing applies.
	req.Body = nil
	if m := c.Match(req); m != nil {
		t.Fatalf("multipart matched %s", m.Rule.Name)
	}
	req.Body = []byte(`{"model":"gpt-6","messages":[{"role":"system","content":"x"}]}`)
	if m := c.Match(req); m != nil {
		t.Fatalf("no user message matched %s", m.Rule.Name)
	}
}

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-8[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestUpstreamIdentity(t *testing.T) {
	for i := 0; i < 64; i++ {
		v := uuidV8(upstreamID(k(i)))
		u, err := uuid.Parse(v)
		if !uuidRE.MatchString(v) || err != nil || u.Version() != 8 || u.Variant() != uuid.RFC4122 {
			t.Fatalf("uuid %q: %v version %d variant %v", v, err, u.Version(), u.Variant())
		}
	}

	clk := &clock{t: time.Unix(3_000_000, 0)}
	svc := newTestService(t, `{"rules":[
	  {"name":"inject","model_regex":["^gpt-"],"key_sources":[{"type":"gjson","path":"k"}],"inject_prompt_cache_key":true,"inject_session_header":"Session_id","session_mode":"off"},
	  {"name":"plain","key_sources":[{"type":"gjson","path":"k"}]}
	]}`, clk)
	ctx := context.Background()
	user := uuid.New()
	req := Request{UserID: user, Model: "gpt-6", Body: []byte(`{"k":"conv-1"}`)}
	s := svc.Begin(ctx, req)
	key := s.PromptCacheKey()
	name, val := s.SessionHeader()
	if !regexp.MustCompile(`^og-[0-9a-f]{32}$`).MatchString(key) || name != "Session_id" || !uuidRE.MatchString(val) {
		t.Fatalf("identity = %q, %s: %q", key, name, val)
	}
	// Never the raw value; stable per conversation (mode off applies too);
	// different for other conversations and other users.
	if strings.Contains(key+val, "conv-1") {
		t.Fatal("raw session value sent upstream")
	}
	if s2 := svc.Begin(ctx, req); s2.PromptCacheKey() != key {
		t.Fatal("prompt_cache_key not stable")
	}
	other := svc.Begin(ctx, Request{UserID: user, Model: "gpt-6", Body: []byte(`{"k":"conv-2"}`)})
	stranger := svc.Begin(ctx, Request{UserID: uuid.New(), Model: "gpt-6", Body: []byte(`{"k":"conv-1"}`)})
	if other.PromptCacheKey() == key || stranger.PromptCacheKey() == key {
		t.Fatal("identity shared across conversations or users")
	}
	if _, v := other.SessionHeader(); v == val {
		t.Fatal("session header shared across conversations")
	}
	// A rule without the options injects nothing; nor does a nil session.
	plain := svc.Begin(ctx, Request{UserID: user, Model: "claude-x", Body: []byte(`{"k":"conv-1"}`)})
	if n, _ := plain.SessionHeader(); plain.Rule != "plain" || plain.PromptCacheKey() != "" || n != "" {
		t.Fatalf("plain rule injects: %+v", plain)
	}
	var none *Session
	if n, _ := none.SessionHeader(); none.PromptCacheKey() != "" || n != "" {
		t.Fatal("nil session injects")
	}
}
