package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"omnigate/internal/channel"
	"omnigate/internal/protocol"
)

func rt(name, typ string, priority, weight int, proto string) *channel.Runtime {
	return &channel.Runtime{Channel: channel.Channel{ID: uuid.New(), Name: name, Type: typ, Priority: priority, Weight: weight,
		Models: []channel.ModelMap{{Model: "aaa", UpstreamModel: "aaa", UpstreamProtocol: proto}}}}
}

func names(list []*channel.Runtime) string {
	s := ""
	for i, r := range list {
		if i > 0 {
			s += ","
		}
		s += r.Name
	}
	return s
}

func TestOrderPrefersNativeProtocol(t *testing.T) {
	anth := rt("anthropic", channel.TypeAnthropic, 0, 100, "")
	chat := rt("chat", channel.TypeOpenAI, 0, 100, "")
	resp := rt("responses", channel.TypeOpenAI, 0, 1, "responses")
	for range 200 {
		if got := names(order([]*channel.Runtime{anth, chat, resp}, protocol.OpenAIResponses, "aaa")); got != "responses,chat,anthropic" {
			t.Fatalf("responses client order = %s", got)
		}
		if got := names(order([]*channel.Runtime{resp, chat, anth}, protocol.Anthropic, "aaa")); got != "anthropic,chat,responses" {
			t.Fatalf("anthropic client order = %s", got)
		}
	}
	// Explicit priority still wins over protocol fit.
	high := rt("high-anthropic", channel.TypeAnthropic, 10, 1, "")
	if got := names(order([]*channel.Runtime{resp, high}, protocol.OpenAIResponses, "aaa")); got != "high-anthropic,responses" {
		t.Fatalf("priority order = %s", got)
	}
	// Within the same protocol tier, weights still spread traffic.
	a, b := rt("a", channel.TypeOpenAI, 0, 1, ""), rt("b", channel.TypeOpenAI, 0, 1, "")
	firstA := 0
	for range 1000 {
		if order([]*channel.Runtime{a, b}, protocol.OpenAIChat, "aaa")[0] == a {
			firstA++
		}
	}
	if firstA < 400 || firstA > 600 {
		t.Fatalf("equal weights should split ~50/50, got %d/1000", firstA)
	}
}

func TestCollapseDuplicateV1(t *testing.T) {
	var got string
	h := CollapseDuplicateV1(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = r.URL.Path }))
	for in, want := range map[string]string{"/v1/v1/messages": "/v1/messages", "/v1/messages": "/v1/messages", "/api/v1/v1/x": "/api/v1/v1/x"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, in, nil))
		if got != want {
			t.Errorf("%s -> %s, want %s", in, got, want)
		}
	}
}
