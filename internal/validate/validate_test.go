package validate

import (
	"strings"
	"testing"
)

func base() []map[string]any {
	return []map[string]any{
		{"type": "mode", "mode": "normal", "sessionId": "11111111-2222-4333-8444-555555555555"},
		{"type": "user", "uuid": "u1", "parentUuid": nil, "sessionId": "11111111-2222-4333-8444-555555555555", "cwd": "/tmp/x", "timestamp": "2026-01-01T00:00:00.000Z", "message": map[string]any{"role": "user", "content": "hi"}},
		{"type": "assistant", "uuid": "a1", "parentUuid": "u1", "sessionId": "11111111-2222-4333-8444-555555555555", "cwd": "/tmp/x", "timestamp": "2026-01-01T00:00:01.000Z", "message": map[string]any{
			"id": "msg_1", "role": "assistant", "usage": map[string]any{}, "content": []any{
				map[string]any{"type": "tool_use", "id": "call1", "name": "Bash", "input": map[string]any{"command": "true"}},
			},
		}},
		{"type": "user", "uuid": "u2", "parentUuid": "a1", "sessionId": "11111111-2222-4333-8444-555555555555", "cwd": "/tmp/x", "timestamp": "2026-01-01T00:00:02.000Z", "message": map[string]any{
			"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "call1", "content": []any{map[string]any{"type": "text", "text": "ok"}}},
			},
		}},
	}
}

func TestValidateOK(t *testing.T) {
	if errs := Validate(base(), "11111111-2222-4333-8444-555555555555"); len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
}

func TestValidateBrokenChain(t *testing.T) {
	rows := base()
	rows[2]["parentUuid"] = "missing"
	errs := Validate(rows, "11111111-2222-4333-8444-555555555555")
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "parentUuid") {
		t.Fatalf("expected parentUuid error, got %v", errs)
	}
}

func TestValidateOrphanToolResult(t *testing.T) {
	rows := base()
	rows[3]["message"].(map[string]any)["content"] = []any{
		map[string]any{"type": "tool_result", "tool_use_id": "nope", "content": "x"},
	}
	errs := Validate(rows, "11111111-2222-4333-8444-555555555555")
	if len(errs) == 0 {
		t.Fatal("expected orphan tool_result error")
	}
}

func TestValidateMissingToolResult(t *testing.T) {
	rows := base()[:3]
	errs := Validate(rows, "11111111-2222-4333-8444-555555555555")
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "no tool_result") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected missing tool_result error, got %v", errs)
	}
}

func TestValidateThinkingSignature(t *testing.T) {
	rows := base()
	rows[2]["message"].(map[string]any)["content"] = []any{
		map[string]any{"type": "thinking", "thinking": "x"},
	}
	errs := Validate(base(), "11111111-2222-4333-8444-555555555555")
	_ = errs
	errs = Validate(rows, "11111111-2222-4333-8444-555555555555")
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "signature") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected signature error, got %v", errs)
	}
}

func TestValidateSidechain(t *testing.T) {
	rows := base()
	rows[1]["isSidechain"] = true
	errs := Validate(rows, "11111111-2222-4333-8444-555555555555")
	if len(errs) == 0 {
		t.Fatal("expected isSidechain error")
	}
}
