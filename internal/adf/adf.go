// Package adf provides minimal helpers for Atlassian Document Format (ADF),
// the rich-text format Jira Cloud REST v3 uses for worklog comments. We only
// need to (a) build a simple single-paragraph document from plain text and
// (b) extract plain text back out of a comment that may be either a plain
// string (v2 style) or an ADF document (v3 style).
package adf

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Doc builds a minimal ADF document containing a single paragraph with the
// given plain text.
func Doc(text string) map[string]any {
	paragraph := map[string]any{"type": "paragraph"}
	if text != "" {
		paragraph["content"] = []any{
			map[string]any{"type": "text", "text": text},
		}
	}
	return map[string]any{
		"type":    "doc",
		"version": 1,
		"content": []any{paragraph},
	}
}

// Text extracts plain text from a comment value that may be a JSON string or
// an ADF document. Unknown/empty input yields an empty string.
func Text(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return ""
	}
	// Plain string comment (v2 style).
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err == nil {
			return s
		}
	}
	// ADF document (v3 style).
	var node map[string]any
	if err := json.Unmarshal(trimmed, &node); err != nil {
		return ""
	}
	var sb strings.Builder
	walk(node, &sb)
	return strings.TrimSpace(sb.String())
}

func walk(node map[string]any, sb *strings.Builder) {
	if t, _ := node["type"].(string); t == "text" {
		if s, ok := node["text"].(string); ok {
			sb.WriteString(s)
		}
	}
	if content, ok := node["content"].([]any); ok {
		for _, c := range content {
			if child, ok := c.(map[string]any); ok {
				walk(child, sb)
			}
		}
		// Separate block-level nodes with a space for readability.
		if t, _ := node["type"].(string); t == "paragraph" {
			sb.WriteString(" ")
		}
	}
}
