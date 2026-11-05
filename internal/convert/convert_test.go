package convert

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pi2claude/internal/id"
	"pi2claude/internal/pi"
	"pi2claude/internal/validate"
)

func load(t *testing.T, rows ...map[string]any) *pi.Session {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "s.jsonl")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(append(b, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := pi.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func header(cwd string) map[string]any {
	return map[string]any{
		"type": "session", "version": 3, "id": "11111111-2222-4333-8444-555555555555",
		"timestamp": "2026-01-01T00:00:00.000Z", "cwd": cwd,
	}
}

func msg(id, parent string, ts string, role string, content any) map[string]any {
	var pid any
	if parent != "" {
		pid = parent
	}
	return map[string]any{
		"type": "message", "id": id, "parentId": pid, "timestamp": ts,
		"message": map[string]any{"role": role, "content": content, "timestamp": int64(1)},
	}
}

func testOptions() Options {
	return Options{
		SessionID: "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
		CWD:       "/tmp/pi2claude-test",
		CCVersion: "2.1.318",
		Model:     "claude-sonnet-5-5",
		Thinking:  "signed",
		ToolMap:   true,
	}
}

func TestConvertBasic(t *testing.T) {
	s := load(t,
		header("/tmp/pi2claude-test"),
		msg("a", "", "2026-01-01T00:00:01.000Z", "user", "hello"),
		msg("b", "a", "2026-01-01T00:00:02.000Z", "assistant", []any{
			map[string]any{"type": "thinking", "thinking": "plan", "thinkingSignature": "CAQSabcdef"},
			map[string]any{"type": "text", "text": "I will run it."},
			map[string]any{"type": "toolCall", "id": "call1", "name": "bash", "arguments": map[string]any{"command": "echo hi"}},
		}),
		map[string]any{"type": "message", "id": "c", "parentId": "b", "timestamp": "2026-01-01T00:00:03.000Z", "message": map[string]any{
			"role": "toolResult", "toolCallId": "call1", "toolName": "bash", "isError": false,
			"content": []any{map[string]any{"type": "text", "text": "hi"}},
		}},
		msg("d", "c", "2026-01-01T00:00:04.000Z", "assistant", []any{map[string]any{"type": "text", "text": "done"}}),
		map[string]any{"type": "session_info", "id": "e", "parentId": "d", "timestamp": "2026-01-01T00:00:05.000Z", "name": "Demo"},
	)
	// Pi's provider lives on the assistant message; the test helper above omits it.
	s.ByID["b"].Message.Provider = "anthropic"
	r, err := Convert(s, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	if errs := validate.Validate(r.Rows, r.SessionID); len(errs) > 0 {
		t.Fatalf("validation: %v\n%s", errs, r.JSONL)
	}
	if r.Rows[0]["type"] != "pi-migration" {
		t.Fatalf("first row = %v", r.Rows[0]["type"])
	}
	if r.Title != "Demo" {
		t.Fatalf("title = %q", r.Title)
	}
	var sawThinking, sawToolUse, sawToolResult, sawLast, sawTitle bool
	var toolUUID string
	for _, row := range r.Rows {
		switch row["type"] {
		case "assistant":
			m := row["message"].(map[string]any)
			for _, raw := range m["content"].([]any) {
				b := raw.(map[string]any)
				switch b["type"] {
				case "thinking":
					if b["signature"] != "CAQSabcdef" {
						t.Fatalf("thinking signature lost: %v", b)
					}
					sawThinking = true
				case "tool_use":
					if b["name"] != "Bash" {
						t.Fatalf("tool name = %v", b["name"])
					}
					in := b["input"].(map[string]any)
					if in["command"] != "echo hi" {
						t.Fatalf("tool input = %v", in)
					}
					sawToolUse = true
					toolUUID = row["uuid"].(string)
				}
			}
		case "user":
			m := row["message"].(map[string]any)
			if arr, ok := m["content"].([]any); ok {
				for _, raw := range arr {
					b := raw.(map[string]any)
					if b["type"] == "tool_result" {
						sawToolResult = true
						if row["sourceToolAssistantUUID"] != toolUUID {
							t.Fatalf("sourceToolAssistantUUID = %v, want %v", row["sourceToolAssistantUUID"], toolUUID)
						}
					}
				}
			}
		case "last-prompt":
			sawLast = true
			if row["leafUuid"] == "" {
				t.Fatal("last-prompt has no leafUuid")
			}
		case "custom-title":
			sawTitle = true
		}
	}
	if !sawThinking || !sawToolUse || !sawToolResult || !sawLast || !sawTitle {
		t.Fatalf("missing rows: thinking=%v tool=%v result=%v last=%v title=%v\n%s",
			sawThinking, sawToolUse, sawToolResult, sawLast, sawTitle, r.JSONL)
	}
	if r.Stats.ToolCalls != 1 || r.Stats.ToolResults != 1 {
		t.Fatalf("stats = %+v", r.Stats)
	}
}

func TestConvertDropsUnsignedThinking(t *testing.T) {
	s := load(t,
		header("/tmp/pi2claude-test"),
		msg("a", "", "2026-01-01T00:00:01.000Z", "user", "hello"),
		msg("b", "a", "2026-01-01T00:00:02.000Z", "assistant", []any{
			map[string]any{"type": "thinking", "thinking": "plan", "thinkingSignature": `{"id":"openai"}`},
			map[string]any{"type": "text", "text": "visible"},
		}),
	)
	s.ByID["b"].Message.Provider = "commandcode"
	r, err := Convert(s, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	if r.Stats.ThinkingKept != 0 || r.Stats.ThinkingDropped != 1 {
		t.Fatalf("thinking stats = %+v", r.Stats)
	}
	if strings.Contains(string(r.JSONL), "plan") {
		t.Fatalf("unsigned thinking leaked:\n%s", r.JSONL)
	}
}

func TestConvertDropsUnmatchedToolCall(t *testing.T) {
	s := load(t,
		header("/tmp/pi2claude-test"),
		msg("a", "", "2026-01-01T00:00:01.000Z", "user", "hello"),
		msg("b", "a", "2026-01-01T00:00:02.000Z", "assistant", []any{
			map[string]any{"type": "text", "text": "calling"},
			map[string]any{"type": "toolCall", "id": "call1", "name": "bash", "arguments": map[string]any{"command": "true"}},
		}),
	)
	r, err := Convert(s, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	if errs := validate.Validate(r.Rows, r.SessionID); len(errs) > 0 {
		t.Fatalf("validation: %v", errs)
	}
	if strings.Contains(string(r.JSONL), "call1") {
		t.Fatalf("unmatched call leaked:\n%s", r.JSONL)
	}
	if r.Stats.ToolCallsDropped != 1 {
		t.Fatalf("dropped = %d", r.Stats.ToolCallsDropped)
	}
}

func TestConvertCompaction(t *testing.T) {
	s := load(t,
		header("/tmp/pi2claude-test"),
		msg("a", "", "2026-01-01T00:00:01.000Z", "user", "old"),
		msg("b", "a", "2026-01-01T00:00:02.000Z", "assistant", []any{map[string]any{"type": "text", "text": "old answer"}}),
		map[string]any{"type": "compaction", "id": "c", "parentId": "b", "timestamp": "2026-01-01T00:00:03.000Z", "summary": "the summary", "firstKeptEntryId": "c", "tokensBefore": 1234},
		msg("d", "c", "2026-01-01T00:00:04.000Z", "user", "new"),
		msg("e", "d", "2026-01-01T00:00:05.000Z", "assistant", []any{map[string]any{"type": "text", "text": "new answer"}}),
	)
	r, err := Convert(s, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	if errs := validate.Validate(r.Rows, r.SessionID); len(errs) > 0 {
		t.Fatalf("validation: %v", errs)
	}
	if !strings.Contains(string(r.JSONL), "compact_boundary") || !strings.Contains(string(r.JSONL), "the summary") {
		t.Fatalf("compaction missing:\n%s", r.JSONL)
	}
	if strings.Contains(string(r.JSONL), "old answer") {
		t.Fatalf("pre-compaction message leaked:\n%s", r.JSONL)
	}
}

func TestToolMappingQuestionnaire(t *testing.T) {
	name, input := mapToolCall("questionnaire", json.RawMessage(`{
		"title":"T",
		"questions":[{"id":"x","label":"Pick","prompt":"Which?","options":[{"label":"A","value":"a","description":"first"}]}]
	}`), true)
	if name != "AskUserQuestion" {
		t.Fatalf("name = %q", name)
	}
	b, _ := json.Marshal(input)
	if !strings.Contains(string(b), `"question":"Which?"`) || !strings.Contains(string(b), `"header":"Pick"`) {
		t.Fatalf("input = %s", b)
	}
}

func TestModelMapping(t *testing.T) {
	if got := (&converter{opts: Options{Model: "claude-sonnet-5-5"}, seenModel: map[string]bool{}}).modelFor("deepseek/deepseek-v4"); got != "claude-sonnet-5-5" {
		t.Fatalf("mapped = %q", got)
	}
	if got := (&converter{opts: Options{Model: "claude-sonnet-5-5"}, seenModel: map[string]bool{}}).modelFor("claude-opus-5-5"); got != "claude-opus-5-5" {
		t.Fatalf("anthropic model changed to %q", got)
	}
}

func TestDeterministicOutput(t *testing.T) {
	s := load(t,
		header("/tmp/pi2claude-test"),
		msg("a", "", "2026-01-01T00:00:01.000Z", "user", "hello"),
		msg("b", "a", "2026-01-01T00:00:02.000Z", "assistant", []any{map[string]any{"type": "text", "text": "hi"}}),
	)
	r1, err := Convert(s, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	r2, err := Convert(s, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	if string(r1.JSONL) != string(r2.JSONL) {
		t.Fatal("output is not deterministic")
	}
	if !id.Valid(r1.SessionID) {
		t.Fatal("session id is not valid")
	}
}
