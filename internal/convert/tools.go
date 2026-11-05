package convert

import (
	"bytes"
	"encoding/json"
	"strings"
)

// mapToolCall maps a Pi tool call to Claude Code's historical tool name and
// input shape where that mapping is lossless. Otherwise it keeps the Pi name
// and arguments: a past tool_use block is never re-executed, so preserving it
// is safer than inventing a shape Claude Code's renderer cannot understand.
func mapToolCall(name string, raw json.RawMessage, enabled bool) (string, any) {
	args := parseArgs(raw)
	if !enabled {
		return name, args
	}
	switch name {
	case "bash":
		out := map[string]any{}
		copyIf(out, args, "command")
		copyIf(out, args, "description")
		copyIf(out, args, "timeout")
		copyIf(out, args, "run_in_background")
		return "Bash", out
	case "read":
		out := map[string]any{}
		copyIf(out, args, "offset")
		copyIf(out, args, "limit")
		if v, ok := args["path"]; ok {
			out["file_path"] = v
		}
		return "Read", out
	case "write":
		out := map[string]any{}
		copyIf(out, args, "content")
		if v, ok := args["path"]; ok {
			out["file_path"] = v
		}
		return "Write", out
	case "edit":
		out, ok := editInput(args)
		if !ok {
			return name, args
		}
		return "Edit", out
	case "grep":
		out := map[string]any{}
		copyIf(out, args, "pattern")
		copyIf(out, args, "path")
		copyIf(out, args, "glob")
		copyIf(out, args, "type")
		copyIf(out, args, "-i")
		copyIf(out, args, "-n")
		copyIf(out, args, "multiline")
		if v, ok := args["limit"]; ok {
			out["head_limit"] = v
		}
		return "Grep", out
	case "find":
		out := map[string]any{}
		copyIf(out, args, "pattern")
		copyIf(out, args, "path")
		return "Glob", out
	case "web_search":
		if qs, ok := args["queries"].([]any); ok && len(qs) == 1 {
			if q, ok := qs[0].(string); ok && q != "" {
				return "WebSearch", map[string]any{"query": q}
			}
		}
		return name, args
	case "fetch_content":
		if urls, ok := args["urls"].([]any); ok && len(urls) == 1 {
			if u, ok := urls[0].(string); ok && u != "" {
				out := map[string]any{"url": u, "prompt": "Fetch and extract the relevant content."}
				copyIf(out, args, "prompt")
				return "WebFetch", out
			}
		}
		return name, args
	case "ask":
		return "AskUserQuestion", askInput(args)
	case "questionnaire":
		return "AskUserQuestion", questionnaireInput(args)
	}
	return name, args
}

func parseArgs(raw json.RawMessage) map[string]any {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return map[string]any{}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return map[string]any{}
	}
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func copyIf(dst, src map[string]any, key string) {
	if v, ok := src[key]; ok && v != nil {
		dst[key] = v
	}
}

func editInput(args map[string]any) (map[string]any, bool) {
	path, _ := args["path"].(string)
	if path == "" {
		return nil, false
	}
	if edits, ok := args["edits"].([]any); ok {
		if len(edits) != 1 {
			return nil, false
		}
		e, ok := edits[0].(map[string]any)
		if !ok {
			return nil, false
		}
		oldText, _ := e["oldText"].(string)
		newText, _ := e["newText"].(string)
		if oldText == "" && newText == "" {
			return nil, false
		}
		out := map[string]any{
			"file_path":  path,
			"old_string": oldText,
			"new_string": newText,
		}
		if v, ok := e["replaceAll"]; ok && v != nil {
			out["replace_all"] = v
		}
		return out, true
	}
	oldText, _ := args["oldText"].(string)
	newText, _ := args["newText"].(string)
	if oldText == "" && newText == "" {
		return nil, false
	}
	return map[string]any{
		"file_path":  path,
		"old_string": oldText,
		"new_string": newText,
	}, true
}

func askInput(args map[string]any) map[string]any {
	q, _ := args["prompt"].(string)
	if q == "" {
		q, _ = args["question"].(string)
	}
	opts := askOptions(args["options"])
	multi := false
	if v, ok := args["multiSelect"].(bool); ok {
		multi = v
	}
	question := map[string]any{
		"question":    q,
		"header":      "Question",
		"multiSelect": multi,
	}
	if len(opts) > 0 {
		question["options"] = opts
	}
	return map[string]any{"questions": []any{question}}
}

func questionnaireInput(args map[string]any) map[string]any {
	var out []any
	if qs, ok := args["questions"].([]any); ok {
		for _, raw := range qs {
			q, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			prompt, _ := q["prompt"].(string)
			if prompt == "" {
				prompt, _ = q["question"].(string)
			}
			label, _ := q["label"].(string)
			if label == "" {
				label, _ = q["header"].(string)
			}
			item := map[string]any{
				"question":    prompt,
				"header":      label,
				"multiSelect": false,
			}
			if v, ok := q["multiSelect"].(bool); ok {
				item["multiSelect"] = v
			}
			if opts := askOptions(q["options"]); len(opts) > 0 {
				item["options"] = opts
			}
			out = append(out, item)
		}
	}
	if len(out) == 0 {
		// Fall back to a single question rather than emitting an invalid shape.
		return askInput(args)
	}
	return map[string]any{"questions": out}
}

func askOptions(raw any) []any {
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	var out []any
	for _, x := range arr {
		m, ok := x.(map[string]any)
		if !ok {
			continue
		}
		label, _ := m["label"].(string)
		if label == "" {
			label, _ = m["value"].(string)
		}
		if strings.TrimSpace(label) == "" {
			continue
		}
		o := map[string]any{"label": label}
		if d, _ := m["description"].(string); d != "" {
			o["description"] = d
		}
		out = append(out, o)
	}
	return out
}
