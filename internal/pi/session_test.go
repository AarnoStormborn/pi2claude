package pi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeSession(t *testing.T, rows ...map[string]any) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "session.jsonl")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, r := range rows {
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(append(b, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func baseRows(cwd string) []map[string]any {
	return []map[string]any{
		{"type": "session", "version": 3, "id": "11111111-2222-4333-8444-555555555555", "timestamp": "2026-01-01T00:00:00.000Z", "cwd": cwd},
	}
}

func TestBuildContextLinear(t *testing.T) {
	rows := baseRows("/tmp/x")
	rows = append(rows,
		map[string]any{"type": "message", "id": "a", "parentId": nil, "timestamp": "2026-01-01T00:00:01.000Z", "message": map[string]any{"role": "user", "content": "hi"}},
		map[string]any{"type": "message", "id": "b", "parentId": "a", "timestamp": "2026-01-01T00:00:02.000Z", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "hello"}}}},
		map[string]any{"type": "message", "id": "c", "parentId": "b", "timestamp": "2026-01-01T00:00:03.000Z", "message": map[string]any{"role": "user", "content": "next"}},
	)
	s, err := Load(writeSession(t, rows...))
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := s.BuildContext()
	if err != nil {
		t.Fatal(err)
	}
	if len(ctx.Items) != 3 {
		t.Fatalf("items = %d, want 3", len(ctx.Items))
	}
	if msgs := ctx.Messages(); len(msgs) != 3 {
		t.Fatalf("messages = %d", len(msgs))
	}
}

func TestBuildContextCompactionAndEdit(t *testing.T) {
	rows := baseRows("/tmp/x")
	rows = append(rows,
		map[string]any{"type": "message", "id": "a", "parentId": nil, "timestamp": "2026-01-01T00:00:01.000Z", "message": map[string]any{"role": "user", "content": "old"}},
		map[string]any{"type": "message", "id": "b", "parentId": "a", "timestamp": "2026-01-01T00:00:02.000Z", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "old answer"}}}},
		map[string]any{"type": "compaction", "id": "c", "parentId": "b", "timestamp": "2026-01-01T00:00:03.000Z", "summary": "summary", "firstKeptEntryId": "d", "tokensBefore": 100},
		map[string]any{"type": "message", "id": "d", "parentId": "c", "timestamp": "2026-01-01T00:00:04.000Z", "message": map[string]any{"role": "user", "content": "kept"}},
		map[string]any{"type": "context_edit", "id": "e", "parentId": "d", "timestamp": "2026-01-01T00:00:05.000Z", "targetId": "d", "replacement": map[string]any{"content": "edited"}},
		map[string]any{"type": "message", "id": "f", "parentId": "e", "timestamp": "2026-01-01T00:00:06.000Z", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "done"}}}},
	)
	s, err := Load(writeSession(t, rows...))
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := s.BuildContext()
	if err != nil {
		t.Fatal(err)
	}
	// compaction + kept user + assistant; old a/b are summarised away.
	if len(ctx.Items) != 3 {
		t.Fatalf("items = %d, want 3: %#v", len(ctx.Items), itemTypes(ctx))
	}
	if ctx.Items[1].Content == nil || string(ctx.Items[1].Content) != `"edited"` {
		t.Fatalf("context edit not applied: %s", ctx.Items[1].Content)
	}
}

func TestBuildContextOmitEdit(t *testing.T) {
	rows := baseRows("/tmp/x")
	rows = append(rows,
		map[string]any{"type": "message", "id": "a", "parentId": nil, "timestamp": "2026-01-01T00:00:01.000Z", "message": map[string]any{"role": "user", "content": "secret"}},
		map[string]any{"type": "context_edit", "id": "b", "parentId": "a", "timestamp": "2026-01-01T00:00:02.000Z", "targetId": "a", "replacement": nil},
		map[string]any{"type": "message", "id": "c", "parentId": "b", "timestamp": "2026-01-01T00:00:03.000Z", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "ok"}}}},
	)
	s, err := Load(writeSession(t, rows...))
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := s.BuildContext()
	if err != nil {
		t.Fatal(err)
	}
	if len(ctx.Items) != 2 || ctx.Items[0].Entry.ID != "a" || !ctx.Items[0].Omit {
		t.Fatalf("omit edit not applied: %#v", ctx.Items)
	}
}

func TestDecodeContent(t *testing.T) {
	text, isString, blocks, err := DecodeContent(json.RawMessage(`"hello"`))
	if err != nil || !isString || text != "hello" || len(blocks) != 0 {
		t.Fatalf("string content: %v %v %q %v", err, isString, text, blocks)
	}
	_, isString, blocks, err = DecodeContent(json.RawMessage(`[{"type":"text","text":"a"}]`))
	if err != nil || isString || len(blocks) != 1 || blocks[0].Text != "a" {
		t.Fatalf("array content: %v %v %v", err, isString, blocks)
	}
}

func TestMissingHeader(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.jsonl")
	if err := os.WriteFile(p, []byte("{\"type\":\"message\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Fatal("expected missing-header error")
	}
}

func itemTypes(ctx *Context) []string {
	var out []string
	for _, i := range ctx.Items {
		out = append(out, i.Entry.Type+":"+i.Entry.ID)
	}
	return out
}
