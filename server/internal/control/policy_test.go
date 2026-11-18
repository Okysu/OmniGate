package control

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPolicySemantics(t *testing.T) {
	if len(categories(DefaultConfig().Categories)) != 6 || len(categories([]string{})) != 0 {
		t.Fatal("default/empty categories")
	}
	lines := []string{}
	for range 40 {
		lines = append(lines, "名称 | 问题 | 是 | 否 | 补充")
	}
	c := categories(lines)
	if len(c) != 32 || c[0].No != "否 | 补充" || c[31].ID != "c31" {
		t.Fatalf("categories=%+v", c)
	}
	c = categories([]string{"", "是否索要敏感操作"})
	if len(c) != 1 || c[0].Ask != c[0].Label {
		t.Fatalf("single=%+v", c)
	}
	c = categories([]string{"低", "高"})
	scores, reason, p, err := judgment([]byte(`{"answers":{"c0":{"probability":0.7},"c1":{"noul":0.98}}}`), c, .7)
	if err != nil || len(scores) != 2 || reason != "高" || p == nil || *p != .98 {
		t.Fatalf("verdict=%v %s %v %v", scores, reason, p, err)
	}
	for _, raw := range []string{`not-json`, `{"answers":{"c0":{"noul":1}}}`, `{"answers":{"c0":{"noul":"1"},"c1":{"noul":1}}}`} {
		if _, _, _, err = judgment([]byte(raw), c, .7); err == nil {
			t.Fatalf("invalid response accepted %s", raw)
		}
	}
	s := fit(strings.Repeat("头", 9000) + "结尾问题")
	if len([]rune(s)) != 8000 || !strings.HasPrefix(s, "头") || !strings.HasSuffix(s, "结尾问题") || !strings.Contains(s, "[truncated]") {
		t.Fatal("head/tail truncation")
	}
}

func TestExtractSupportedTextAndExcludesMedia(t *testing.T) {
	cases := []struct{ raw, want string }{
		{`{"messages":[{"role":"user","content":"你好"}]}`, "你好"},
		{`{"input":[{"role":"user","content":[{"type":"input_text","text":"响应文本"},{"type":"input_image","image_url":"https://secret/image"}]}]}`, "响应文本"},
		{`{"system":"系统说明","messages":[{"role":"assistant","content":[{"type":"tool_use","name":"执行工具","input":{"command":"ls"}}]}]}`, "command"},
		{`{"messages":[{"role":"assistant","tool_calls":[{"function":{"name":"run","arguments":"{\"x\":1}"}}]}]}`, "run"},
		{`{"prompt":"画一棵树","suffix":"结尾"}`, "画一棵树"},
		{`{"input":["嵌入文本",[123,456]]}`, "嵌入文本"},
		{`{"input":"朗读文本","instructions":"语音要求"}`, "朗读文本"},
		{`{"query":"重排查询","documents":["文档一","文档二"]}`, "文档二"},
		{`{"state":{"nested":{"text":"状态文本"}}}`, "状态文本"},
	}
	for _, c := range cases {
		if got := Extract([]byte(c.raw)); !strings.Contains(got, c.want) || strings.Contains(got, "secret/image") {
			t.Fatalf("extract=%q from %s", got, c.raw)
		}
	}
	for _, raw := range []string{`{"input":[123,456]}`, `{"input":[{"type":"input_audio","data":"secret-base64"}]}`, `{"tools":[{"description":"secret schema"}],"api_key":"secret-key","model":"private-model"}`, `{"input":"data:image/png;base64,secret"}`} {
		if got := Extract([]byte(raw)); got != "" {
			t.Fatalf("non-text submitted: %q", got)
		}
	}
	raw := []byte(`{"input":"保留请求"}`)
	old := string(raw)
	_ = Extract(raw)
	if string(raw) != old || !json.Valid(raw) {
		t.Fatal("request mutated")
	}
}

func TestRuleChannelAndModelIntersection(t *testing.T) {
	r := Rule{Enabled: true, ChannelIDs: []string{"a"}, Models: []string{"gpt-*"}}
	if !r.matches("a", "gpt-6.2-sol") || r.matches("b", "gpt-6.2-sol") || r.matches("a", "claude-sonnet") {
		t.Fatal("scope mismatch")
	}
	r.Enabled = false
	if r.matches("a", "gpt-6.2-sol") {
		t.Fatal("disabled rule matches")
	}
}

func TestRegexModelPatterns(t *testing.T) {
	r := Rule{Enabled: true, Models: []string{`re:^gpt-6\.[12]-(sol|astra)$`}}
	if err := r.compileModels(); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"gpt-6.1-sol", "gpt-6.2-astra"} {
		if !r.matches("any", m) {
			t.Fatalf("regex did not match %s", m)
		}
	}
	for _, m := range []string{"gpt-6.2-terra", "xgpt-6.2-sol", "gpt-6.3-sol"} {
		if r.matches("any", m) {
			t.Fatalf("regex unexpectedly matches %s", m)
		}
	}
	for _, pattern := range []string{"re:", "re:[", "["} {
		r.Models = []string{pattern}
		if r.compileModels() == nil {
			t.Fatalf("invalid pattern accepted %s", pattern)
		}
	}
}
