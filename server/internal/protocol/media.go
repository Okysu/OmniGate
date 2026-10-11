package protocol

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// Media (images and files) in message content and in tool results.
//
// Inside the Chat pivot a tool message may carry image_url / file parts next
// to its text: Anthropic tool_result blocks and Responses function_call_output
// arrays both allow images and files, and the converters map them onto each
// other through the pivot. A real Chat upstream only accepts text in tool
// messages, so relocateToolMedia moves the media into the following user
// message before a request is sent to one.

// chatContentParts returns string or array content as parts.
func chatContentParts(raw json.RawMessage) ([]ChatPart, error) {
	if isNull(raw) {
		return nil, nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if s == "" {
			return nil, nil
		}
		return []ChatPart{{Type: "text", Text: s}}, nil
	}
	var parts []ChatPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil, fmt.Errorf("content 必须是字符串或内容数组")
	}
	return parts, nil
}

func isMediaPart(p ChatPart) bool { return p.Type == "image_url" || p.Type == "file" }

func hasMedia(parts []ChatPart) bool {
	for _, p := range parts {
		if isMediaPart(p) {
			return true
		}
	}
	return false
}

// parseDataURL splits a base64 data URL into its media type and payload.
func parseDataURL(u string) (mediaType, data string, ok bool) {
	rest, ok := strings.CutPrefix(u, "data:")
	if !ok {
		return "", "", false
	}
	meta, data, ok := strings.Cut(rest, ",")
	mediaType, isB64 := strings.CutSuffix(meta, ";base64")
	if !ok || !isB64 || mediaType == "" {
		return "", "", false
	}
	return mediaType, data, true
}

func defaultFilename(mediaType string) string {
	switch mediaType {
	case "application/pdf":
		return "document.pdf"
	case "text/plain":
		return "document.txt"
	}
	return "file"
}

// chatPartToResponses converts one Chat content part into a Responses input
// part; nil means the part was recorded as unsupported.
func chatPartToResponses(p ChatPart, is *issues) (map[string]any, error) {
	switch p.Type {
	case "text":
		return map[string]any{"type": "input_text", "text": p.Text}, nil
	case "image_url":
		if p.ImageURL == nil || p.ImageURL.URL == "" {
			return nil, fmt.Errorf("image_url 缺少 url")
		}
		c := map[string]any{"type": "input_image", "image_url": p.ImageURL.URL}
		if p.ImageURL.Detail != "" {
			c["detail"] = p.ImageURL.Detail
		}
		return c, nil
	case "file":
		if p.File == nil || p.File.FileID == "" && p.File.FileData == "" {
			return nil, fmt.Errorf("file 缺少 file_id 或 file_data")
		}
		c := map[string]any{"type": "input_file"}
		if p.File.FileID != "" {
			c["file_id"] = p.File.FileID
		}
		if p.File.FileData != "" {
			c["file_data"] = p.File.FileData
			name := p.File.Filename
			if name == "" {
				mt, _, _ := parseDataURL(p.File.FileData)
				name = defaultFilename(mt)
			}
			c["filename"] = name
		} else if p.File.Filename != "" {
			c["filename"] = p.File.Filename
		}
		return c, nil
	}
	is.unsupportedField("content[].type=" + p.Type)
	return nil, nil
}

// responsesPartToChat converts one Responses input part into a Chat content
// part; nil means the part was recorded as unsupported.
func responsesPartToChat(p responsesPart, is *issues) *ChatPart {
	switch p.Type {
	case "input_text", "output_text", "text":
		return &ChatPart{Type: "text", Text: p.Text}
	case "input_image":
		if p.ImageURL == "" {
			is.unsupportedField("input_image.file_id")
			return nil
		}
		return &ChatPart{Type: "image_url", ImageURL: &ChatImageURL{URL: p.ImageURL, Detail: p.Detail}}
	case "input_file":
		if p.FileID == "" && p.FileData == "" {
			is.unsupportedField("input_file.file_url")
			return nil
		}
		return &ChatPart{Type: "file", File: &ChatFile{FileID: p.FileID, FileData: p.FileData, Filename: p.Filename}}
	}
	is.unsupportedField("content[].type=" + p.Type)
	return nil
}

// chatFileToBlock converts a Chat file part into an Anthropic document (or
// image) block; ok=false means it was recorded as unsupported.
func chatFileToBlock(f *ChatFile, is *issues) (AnthropicBlock, bool, error) {
	if f == nil || f.FileID == "" && f.FileData == "" {
		return AnthropicBlock{}, false, fmt.Errorf("file 缺少 file_id 或 file_data")
	}
	if f.FileData == "" {
		is.unsupportedField("file.file_id")
		return AnthropicBlock{}, false, nil
	}
	mt, data, ok := parseDataURL(f.FileData)
	if !ok {
		return AnthropicBlock{}, false, fmt.Errorf("file.file_data 必须是 base64 data URL")
	}
	switch {
	case mt == "application/pdf":
		return AnthropicBlock{Type: "document", Source: &AnthropicSource{Type: "base64", MediaType: mt, Data: data}, Title: f.Filename}, true, nil
	case strings.HasPrefix(mt, "image/"):
		return AnthropicBlock{Type: "image", Source: &AnthropicSource{Type: "base64", MediaType: mt, Data: data}}, true, nil
	case strings.HasPrefix(mt, "text/"):
		raw, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			return AnthropicBlock{}, false, fmt.Errorf("file.file_data 不是合法的 base64")
		}
		return AnthropicBlock{Type: "document", Source: &AnthropicSource{Type: "text", MediaType: "text/plain", Data: string(raw)}, Title: f.Filename}, true, nil
	}
	is.unsupportedField("file.media_type=" + mt)
	return AnthropicBlock{}, false, nil
}

// anthropicDocumentToChat converts an Anthropic document block into a Chat
// part; nil means it was recorded as unsupported.
func anthropicDocumentToChat(bl AnthropicBlock, is *issues) *ChatPart {
	src := bl.Source
	if src == nil {
		is.unsupportedField("document.source")
		return nil
	}
	switch src.Type {
	case "base64":
		name := bl.Title
		if name == "" {
			name = defaultFilename(src.MediaType)
		}
		return &ChatPart{Type: "file", File: &ChatFile{FileData: "data:" + src.MediaType + ";base64," + src.Data, Filename: name}}
	case "text":
		text := src.Data
		if bl.Title != "" {
			text = bl.Title + "\n\n" + text
		}
		return &ChatPart{Type: "text", Text: text}
	}
	is.unsupportedField("document.source.type=" + src.Type)
	return nil
}

// relocateToolMedia moves images and files out of tool messages (a real Chat
// upstream accepts text only there) into the user message that follows the
// tool results, leaving a placeholder in the tool text.
func relocateToolMedia(msgs []ChatMessage) []ChatMessage {
	var out []ChatMessage
	var pending []ChatPart
	flush := func(next *ChatMessage) {
		if len(pending) == 0 {
			return
		}
		if next != nil && next.Role == "user" {
			if parts, err := chatContentParts(next.Content); err == nil {
				next.Content, _ = json.Marshal(append(pending, parts...))
				pending = nil
				return
			}
		}
		out = append(out, chatParts("user", pending))
		pending = nil
	}
	for _, m := range msgs {
		if m.Role == "tool" {
			if parts, err := chatContentParts(m.Content); err == nil && hasMedia(parts) {
				var text strings.Builder
				var media []ChatPart
				images := true
				for _, p := range parts {
					switch {
					case p.Type == "text":
						text.WriteString(p.Text)
					case isMediaPart(p):
						media = append(media, p)
						kind := "image"
						if p.Type == "file" {
							kind, images = "file", false
						}
						if text.Len() > 0 {
							text.WriteString("\n")
						}
						fmt.Fprintf(&text, "[%s %d attached below]", kind, len(media))
					}
				}
				m.Content, _ = json.Marshal(text.String())
				label := "Images"
				if !images {
					label = "Attachments"
				}
				pending = append(pending, ChatPart{Type: "text", Text: fmt.Sprintf("[%s returned by tool call %s]", label, m.ToolCallID)})
				pending = append(pending, media...)
			}
			out = append(out, m)
			continue
		}
		flush(&m)
		out = append(out, m)
	}
	flush(nil)
	return out
}

// relocateToolMediaJSON applies relocateToolMedia to a Chat request body.
func relocateToolMediaJSON(body []byte) []byte {
	var raw map[string]json.RawMessage
	if json.Unmarshal(body, &raw) != nil {
		return body
	}
	var msgs []ChatMessage
	if json.Unmarshal(raw["messages"], &msgs) != nil {
		return body
	}
	moved := false
	for _, m := range msgs {
		if m.Role == "tool" {
			if parts, err := chatContentParts(m.Content); err == nil && hasMedia(parts) {
				moved = true
				break
			}
		}
	}
	if !moved {
		return body
	}
	raw["messages"], _ = json.Marshal(relocateToolMedia(msgs))
	out, err := json.Marshal(raw)
	if err != nil {
		return body
	}
	return out
}
