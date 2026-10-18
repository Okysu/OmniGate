package protocol

import "encoding/json"

// A tool_choice must not outlive its tools: when a converter drops a tool it
// cannot express (e.g. Anthropic's web_search server tool in lenient mode),
// a tool_choice naming that tool — or requiring a tool when none is left —
// is rejected by the upstream ("Tool choice 'web_search' must be specified
// with 'tools' parameter"). Such a tool_choice is dropped with the tool.

// toolChoiceTarget returns the tool a Chat tool_choice names ("" = none) and
// whether it requires calling some tool.
func toolChoiceTarget(tc json.RawMessage) (name string, required bool) {
	var s string
	if json.Unmarshal(tc, &s) == nil {
		return "", s == "required"
	}
	var obj struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
		Name string `json:"name"`
	}
	if json.Unmarshal(tc, &obj) == nil {
		if obj.Function.Name != "" {
			return obj.Function.Name, true
		}
		return obj.Name, obj.Name != ""
	}
	return "", false
}

// danglingToolChoice reports whether a tool_choice naming name (or requiring
// a tool) has no matching tool among names.
func danglingToolChoice(name string, required bool, names []string) bool {
	if name == "" {
		return required && len(names) == 0
	}
	for _, n := range names {
		if n == name {
			return false
		}
	}
	return true
}

func chatToolNames(tools []ChatTool) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Function.Name)
	}
	return out
}

// dropDanglingChatToolChoice clears a Chat request's dangling tool_choice.
func dropDanglingChatToolChoice(out *ChatRequest, is *issues) {
	if len(out.ToolChoice) == 0 || isNull(out.ToolChoice) {
		return
	}
	if name, req := toolChoiceTarget(out.ToolChoice); danglingToolChoice(name, req, chatToolNames(out.Tools)) {
		is.drop("tool_choice")
		out.ToolChoice = nil
	}
}
