// Package validate checks the structural invariants Claude Code's resume
// loader and the Anthropic Messages API require.
package validate

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Validate checks rows in order. expectedSessionID may be empty.
func Validate(rows []map[string]any, expectedSessionID string) []error {
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	seen := make(map[string]bool)
	calls := make(map[string]bool)
	results := make(map[string]bool)
	assistantCount, userCount := 0, 0

	for i, row := range rows {
		line := i + 1
		typ, _ := row["type"].(string)
		if typ == "" {
			add("line %d: missing type", line)
			continue
		}
		if sid, ok := row["sessionId"].(string); ok {
			if expectedSessionID != "" && sid != expectedSessionID {
				add("line %d: sessionId %q != %q", line, sid, expectedSessionID)
			}
		}
		if sid, ok := row["session_id"].(string); ok {
			if expectedSessionID != "" && sid != expectedSessionID {
				add("line %d: session_id %q != %q", line, sid, expectedSessionID)
			}
		}
		if cwd, ok := row["cwd"].(string); ok && strings.TrimSpace(cwd) == "" {
			add("line %d: empty cwd", line)
		}
		if side, ok := row["isSidechain"].(bool); ok && side {
			add("line %d: isSidechain must be false in a main transcript", line)
		}
		if ts, ok := row["timestamp"].(string); ok && ts != "" {
			if _, err := time.Parse(time.RFC3339Nano, ts); err != nil {
				if _, err2 := time.Parse("2006-01-02T15:04:05.000Z", ts); err2 != nil {
					add("line %d: bad timestamp %q", line, ts)
				}
			}
		}

		chained := typ == "user" || typ == "assistant" || typ == "system" || typ == "attachment"
		if chained {
			uuid, _ := row["uuid"].(string)
			if uuid == "" {
				add("line %d: %s row has no uuid", line, typ)
			} else {
				if seen[uuid] {
					add("line %d: duplicate uuid %q", line, uuid)
				}
				seen[uuid] = true
			}
			parent, hasParent := row["parentUuid"]
			if hasParent && parent != nil {
				ps, ok := parent.(string)
				if !ok {
					add("line %d: parentUuid is not a string or null", line)
				} else if !seen[ps] {
					add("line %d: parentUuid %q does not refer to an earlier row", line, ps)
				}
			}
		}

		switch typ {
		case "user":
			userCount++
			if err := checkUser(row, calls, results); err != nil {
				add("line %d: %v", line, err)
			}
		case "assistant":
			assistantCount++
			if err := checkAssistant(row, calls); err != nil {
				add("line %d: %v", line, err)
			}
		}
	}

	for id := range calls {
		if !results[id] {
			add("tool_use %q has no tool_result", id)
		}
	}
	if assistantCount == 0 && userCount == 0 {
		add("transcript has no user or assistant rows")
	}
	return errs
}

func checkUser(row map[string]any, calls, results map[string]bool) error {
	msg, ok := row["message"].(map[string]any)
	if !ok {
		return fmt.Errorf("missing message")
	}
	if role, _ := msg["role"].(string); role != "user" {
		return fmt.Errorf("message.role is %q, want user", role)
	}
	content, ok := msg["content"]
	if !ok || content == nil {
		return fmt.Errorf("missing message.content")
	}
	switch v := content.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("empty user content")
		}
	case []any:
		if len(v) == 0 {
			return fmt.Errorf("empty user content array")
		}
		for _, raw := range v {
			b, ok := raw.(map[string]any)
			if !ok {
				return fmt.Errorf("user content block is not an object")
			}
			switch b["type"] {
			case "tool_result":
				id, _ := b["tool_use_id"].(string)
				if id == "" {
					return fmt.Errorf("tool_result has no tool_use_id")
				}
				if !calls[id] {
					return fmt.Errorf("tool_result %q has no earlier tool_use", id)
				}
				const ckey = "content"
				if c, ok := b[ckey]; !ok || c == nil {
					return fmt.Errorf("tool_result %q has no content", id)
				}
				results[id] = true
			case "text":
				if t, _ := b["text"].(string); strings.TrimSpace(t) == "" {
					return fmt.Errorf("empty text block")
				}
			case "image":
				if err := checkImage(b); err != nil {
					return err
				}
			default:
				return fmt.Errorf("unexpected user content block %q", b["type"])
			}
		}
	default:
		return fmt.Errorf("message.content has unexpected type %T", content)
	}
	return nil
}

func checkAssistant(row map[string]any, calls map[string]bool) error {
	msg, ok := row["message"].(map[string]any)
	if !ok {
		return fmt.Errorf("missing message")
	}
	if role, _ := msg["role"].(string); role != "assistant" {
		return fmt.Errorf("message.role is %q, want assistant", role)
	}
	if id, _ := msg["id"].(string); id == "" {
		return fmt.Errorf("missing message.id")
	}
	if _, ok := msg["usage"].(map[string]any); !ok {
		return fmt.Errorf("missing message.usage")
	}
	content, ok := msg["content"].([]any)
	if !ok || len(content) == 0 {
		return fmt.Errorf("missing or empty message.content")
	}
	for _, raw := range content {
		b, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("assistant content block is not an object")
		}
		switch b["type"] {
		case "text":
			if t, _ := b["text"].(string); strings.TrimSpace(t) == "" {
				return fmt.Errorf("empty text block")
			}
		case "thinking":
			if t, _ := b["thinking"].(string); strings.TrimSpace(t) == "" {
				return fmt.Errorf("empty thinking block")
			}
			if s, _ := b["signature"].(string); s == "" {
				return fmt.Errorf("thinking block has no signature")
			}
		case "redacted_thinking":
			if s, _ := b["data"].(string); s == "" {
				return fmt.Errorf("redacted_thinking block has no data")
			}
		case "tool_use":
			id, _ := b["id"].(string)
			if id == "" {
				return fmt.Errorf("tool_use has no id")
			}
			if calls[id] {
				return fmt.Errorf("duplicate tool_use id %q", id)
			}
			calls[id] = true
			if n, _ := b["name"].(string); n == "" {
				return fmt.Errorf("tool_use %q has no name", id)
			}
			if _, ok := b["input"].(map[string]any); !ok {
				return fmt.Errorf("tool_use %q input is not an object", id)
			}
		default:
			return fmt.Errorf("unexpected assistant content block %q", b["type"])
		}
	}
	return nil
}

func checkImage(b map[string]any) error {
	src, ok := b["source"].(map[string]any)
	if !ok {
		return fmt.Errorf("image block has no source")
	}
	if t, _ := src["type"].(string); t != "base64" {
		return fmt.Errorf("image source type is %q, want base64", t)
	}
	if d, _ := src["data"].(string); d == "" {
		return fmt.Errorf("image source has no data")
	}
	if m, _ := src["media_type"].(string); m == "" {
		return fmt.Errorf("image source has no media_type")
	}
	return nil
}

// File reads a JSONL transcript and validates it. The filename's stem is used
// as the expected session id.
func File(path string) ([]map[string]any, []error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, []error{err}
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 256*1024*1024)
	var rows []map[string]any
	var errs []error
	line := 0
	for sc.Scan() {
		line++
		b := sc.Bytes()
		if len(strings.TrimSpace(string(b))) == 0 {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal(b, &row); err != nil {
			errs = append(errs, fmt.Errorf("line %d: invalid JSON: %w", line, err))
			continue
		}
		rows = append(rows, row)
	}
	if err := sc.Err(); err != nil {
		errs = append(errs, err)
	}
	expected := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	errs = append(errs, Validate(rows, expected)...)
	return rows, errs
}
