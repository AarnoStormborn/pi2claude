// Package convert maps a Pi session to a Claude Code JSONL transcript.
package convert

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"pi2claude/internal/id"
	"pi2claude/internal/pi"
)

// Version is stamped into the transcript marker.
const Version = "0.1.0"

// Options controls conversion.
type Options struct {
	SessionID string // optional; must be a canonical UUID when set
	CWD       string // optional cwd override
	CCVersion string
	Model     string // fallback Claude model for non-Anthropic history
	Thinking  string // "signed", "drop", or "text"
	ToolMap   bool
	GitBranch string
}

// Stats summarizes a conversion.
type Stats struct {
	PiEntries          int `json:"piEntries"`
	SelectedEntries    int `json:"selectedEntries"`
	Rows               int `json:"rows"`
	Messages           int `json:"messages"`
	UserMessages       int `json:"userMessages"`
	AssistantMsgs      int `json:"assistantMessages"`
	ToolResults        int `json:"toolResults"`
	ToolCalls          int `json:"toolCalls"`
	ToolCallsDropped   int `json:"toolCallsDropped"`
	ToolResultsDropped int `json:"toolResultsDropped"`
	ThinkingKept       int `json:"thinkingKept"`
	ThinkingDropped    int `json:"thinkingDropped"`
	Images             int `json:"images"`
	Compactions        int `json:"compactions"`
	Bytes              int `json:"bytes"`
}

// Result is a converted session.
type Result struct {
	Rows       []map[string]any
	SessionID  string
	CWD        string
	Title      string
	LastPrompt string
	Warnings   []string
	Stats      Stats
	JSONL      []byte
}

type prepared struct {
	item    *pi.Selected
	kind    string // message, custom_message, branch_summary, compaction
	role    string
	content json.RawMessage
	time    time.Time
}

type converter struct {
	opts        Options
	sessionID   string
	cwd         string
	title       string
	rows        []map[string]any
	lastUUID    string
	lastPrompt  string
	warnings    []string
	stats       Stats
	seenModel   map[string]bool
	toolUseRows map[string]string
	provider    string
	headerTime  time.Time
}

// Convert maps the active Pi context to Claude Code transcript rows.
func Convert(s *pi.Session, opts Options) (*Result, error) {
	if opts.CCVersion == "" {
		opts.CCVersion = "2.1.318"
	}
	if opts.Model == "" {
		opts.Model = "claude-sonnet-5-5"
	}
	if opts.Thinking == "" {
		opts.Thinking = "signed"
	}
	if opts.GitBranch == "" {
		opts.GitBranch = "HEAD"
	}
	sessionID := opts.SessionID
	if sessionID == "" {
		sessionID = id.V5(id.Namespace, "claude-session:"+s.Header.ID)
	}
	if !id.Valid(sessionID) {
		return nil, fmt.Errorf("session id %q is not a canonical UUID", sessionID)
	}
	cwd := opts.CWD
	if cwd == "" {
		cwd = s.Header.CWD
	}
	ctx, err := s.BuildContext()
	if err != nil {
		return nil, err
	}

	headerTime, _ := parseTime(s.Header.Timestamp)
	c := &converter{
		opts:       opts,
		sessionID:  sessionID,
		cwd:        cwd,
		title:      ctx.Title,
		warnings:   append([]string{}, ctx.Warnings...),
		seenModel:  make(map[string]bool),
		headerTime: headerTime,
	}
	c.stats.PiEntries = len(s.Entries)
	c.stats.SelectedEntries = len(ctx.Items)

	items, err := c.prepare(ctx)
	if err != nil {
		return nil, err
	}
	keptCalls := c.pairTools(items)

	// A provenance marker is ignored by Claude Code's loader but lets pi2claude
	// recognize its own output if the manifest is lost.
	c.rows = append(c.rows, map[string]any{
		"type":               "pi-migration",
		"piSessionId":        s.Header.ID,
		"piSessionPath":      s.Path,
		"piSessionTimestamp": s.Header.Timestamp,
		"piSessionVersion":   s.Header.Version,
		"converter":          "pi2claude",
		"converterVersion":   Version,
	})
	c.rows = append(c.rows,
		map[string]any{"type": "mode", "mode": "normal", "sessionId": sessionID},
		map[string]any{"type": "permission-mode", "permissionMode": "default", "sessionId": sessionID},
	)

	for _, p := range items {
		switch p.item.Entry.Type {
		case "message":
			if p.item.Entry.Message == nil {
				continue
			}
			switch p.role {
			case "user":
				c.emitUser(p)
			case "assistant":
				c.provider = p.item.Entry.Message.Provider
				if c.provider == "" && strings.HasPrefix(strings.ToLower(p.item.Entry.Message.Model), "claude-") {
					c.provider = "anthropic"
				}
				c.emitAssistant(p, keptCalls)
				c.provider = ""
			case "toolResult":
				c.emitToolResult(p, keptCalls)
			case "system":
				// Pi's system prompt/tool state is rebuilt by Claude Code.
			case "bashExecution":
				c.emitBash(p)
			case "custom":
				c.emitCustomMessage(p)
			default:
				c.warn("skipped unknown message role %q", p.role)
			}
		case "custom_message":
			c.emitCustomMessage(p)
		case "branch_summary":
			c.emitBranchSummary(p)
		case "compaction":
			c.emitCompaction(p)
		}
	}

	if c.lastUUID == "" {
		return nil, fmt.Errorf("session has no conversational content to migrate")
	}

	if c.lastPrompt == "" {
		c.lastPrompt = c.firstUserPrompt(items)
	}
	c.rows = append(c.rows, map[string]any{
		"type":       "last-prompt",
		"lastPrompt": c.lastPrompt,
		"leafUuid":   c.lastUUID,
		"sessionId":  sessionID,
	})
	if c.title != "" {
		c.rows = append(c.rows, map[string]any{
			"type":        "custom-title",
			"customTitle": c.title,
			"sessionId":   sessionID,
		})
	}

	var buf strings.Builder
	for _, r := range c.rows {
		b, err := json.Marshal(r)
		if err != nil {
			return nil, fmt.Errorf("marshal row: %w", err)
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	c.stats.Rows = len(c.rows)
	c.stats.Bytes = buf.Len()

	return &Result{
		Rows:       c.rows,
		SessionID:  sessionID,
		CWD:        cwd,
		Title:      c.title,
		LastPrompt: c.lastPrompt,
		Warnings:   dedupe(c.warnings),
		Stats:      c.stats,
		JSONL:      []byte(buf.String()),
	}, nil
}

func (c *converter) prepare(ctx *pi.Context) ([]*prepared, error) {
	var out []*prepared
	for _, item := range ctx.Items {
		if item.Omit {
			continue
		}
		e := item.Entry
		p := &prepared{item: item, time: entryTime(e, c.headerTime)}
		switch e.Type {
		case "message":
			if e.Message == nil {
				continue
			}
			p.kind = "message"
			p.role = e.Message.Role
			p.content = item.EntryContent()
			c.stats.Images += countImages(p.content)
			c.stats.Messages++
			switch e.Message.Role {
			case "user":
				c.stats.UserMessages++
			case "assistant":
				c.stats.AssistantMsgs++
			case "toolResult":
				c.stats.ToolResults++
			}
		case "custom_message":
			p.kind = "custom_message"
			p.role = "custom"
			p.content = item.EntryContent()
			c.stats.Images += countImages(p.content)
		case "branch_summary":
			p.kind = "branch_summary"
			p.role = "branchSummary"
		case "compaction":
			p.kind = "compaction"
			p.role = "compaction"
		default:
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// pairTools keeps only tool calls that have a matching result on the active
// branch, and only results whose call survived context edits.
func (c *converter) pairTools(items []*prepared) map[string]bool {
	calls := make(map[string]bool)
	results := make(map[string]bool)
	for _, p := range items {
		if p.kind != "message" {
			continue
		}
		_, _, blocks, err := pi.DecodeContent(p.content)
		if err != nil {
			continue
		}
		switch p.role {
		case "assistant":
			for _, b := range blocks {
				if b.Type == "toolCall" && b.ID != "" {
					calls[b.ID] = true
					c.stats.ToolCalls++
				}
			}
		case "toolResult":
			if id := p.item.Entry.Message.ToolCallID; id != "" {
				results[id] = true
			}
		}
	}
	kept := make(map[string]bool)
	for call := range calls {
		if results[call] {
			kept[call] = true
		} else {
			c.stats.ToolCallsDropped++
			c.warn("dropped tool call %q with no result on the active branch", call)
		}
	}
	for result := range results {
		if !kept[result] {
			c.stats.ToolResultsDropped++
			c.warn("dropped tool result %q whose call is not in context", result)
		}
	}
	return kept
}

func (c *converter) emitUser(p *prepared) {
	content, ok := userContent(p.content)
	if !ok {
		return
	}
	row := c.chainRow(p.time)
	row["type"] = "user"
	row["message"] = map[string]any{"role": "user", "content": content}
	row["origin"] = map[string]any{"kind": "human"}
	row["promptId"] = id.V5(id.Namespace, c.sessionID+":"+p.item.Entry.ID+":prompt")
	row["promptSource"] = "typed"
	row["turnOrigin"] = "human"
	c.rows = append(c.rows, row)
	if text := contentText(content); text != "" {
		c.lastPrompt = text
	}
}

func (c *converter) emitAssistant(p *prepared, keptCalls map[string]bool) {
	m := p.item.Entry.Message
	blocks, err := c.assistantBlocks(p.content)
	if err != nil {
		c.warn("assistant message %q: %v", p.item.Entry.ID, err)
		return
	}
	var ordered []map[string]any
	for _, b := range blocks {
		if b["type"] == "thinking" || b["type"] == "redacted_thinking" {
			ordered = append(ordered, b)
		}
	}
	for _, b := range blocks {
		if b["type"] != "thinking" && b["type"] != "redacted_thinking" {
			ordered = append(ordered, b)
		}
	}
	// A tool call can only be kept if its result survived.
	filtered := ordered[:0]
	for _, b := range ordered {
		if b["type"] == "tool_use" {
			tid, _ := b["id"].(string)
			if !keptCalls[tid] {
				continue
			}
		}
		filtered = append(filtered, b)
	}
	if len(filtered) == 0 {
		return
	}

	model := c.modelFor(m.Model)
	msgID := "msg_" + id.HashHex(24, c.sessionID, p.item.Entry.ID, "message")
	reqID := "req_" + id.HashHex(24, c.sessionID, p.item.Entry.ID, "request")
	usage := claudeUsage(m.Usage)
	stop := stopReason(m.StopReason)
	for i, b := range filtered {
		row := c.chainRow(p.time)
		row["type"] = "assistant"
		row["message"] = map[string]any{
			"id":            msgID,
			"type":          "message",
			"role":          "assistant",
			"model":         model,
			"content":       []any{b},
			"stop_reason":   stop,
			"stop_sequence": nil,
			"usage":         usage,
		}
		row["requestId"] = reqID
		row["requestedModel"] = model
		row["apiBlockIndex"] = i
		c.rows = append(c.rows, row)
		if b["type"] == "tool_use" {
			if tid, _ := b["id"].(string); tid != "" {
				// Remember the assistant record that owns this call.
				c.toolUseRow(tid, row["uuid"].(string))
			}
		}
	}
}

func (c *converter) emitToolResult(p *prepared, keptCalls map[string]bool) {
	m := p.item.Entry.Message
	if !keptCalls[m.ToolCallID] {
		return
	}
	content := toolResultContent(p.content)
	block := map[string]any{
		"type":        "tool_result",
		"tool_use_id": m.ToolCallID,
		"content":     content,
	}
	if m.IsError {
		block["is_error"] = true
	}
	row := c.chainRow(p.time)
	row["type"] = "user"
	row["message"] = map[string]any{"role": "user", "content": []any{block}}
	if src := c.toolUseSource(m.ToolCallID); src != "" {
		row["sourceToolAssistantUUID"] = src
	}
	c.rows = append(c.rows, row)
}

func (c *converter) emitBash(p *prepared) {
	m := p.item.Entry.Message
	if m.ExcludeFromCtx {
		return
	}
	var b strings.Builder
	b.WriteString("<bash-input>")
	b.WriteString(m.Command)
	b.WriteString("</bash-input>")
	if m.Output != "" {
		b.WriteString("\n<bash-stdout>")
		b.WriteString(m.Output)
		b.WriteString("</bash-stdout>")
	}
	row := c.chainRow(p.time)
	row["type"] = "user"
	row["message"] = map[string]any{"role": "user", "content": b.String()}
	row["isMeta"] = true
	c.rows = append(c.rows, row)
}

func (c *converter) emitCustomMessage(p *prepared) {
	content, ok := userContent(p.content)
	if !ok {
		return
	}
	row := c.chainRow(p.time)
	row["type"] = "user"
	row["message"] = map[string]any{"role": "user", "content": content}
	row["isMeta"] = true
	row["origin"] = map[string]any{"kind": "human"}
	if p.item.Entry.CustomType != "" {
		row["piCustomType"] = p.item.Entry.CustomType
	}
	c.rows = append(c.rows, row)
}

func (c *converter) emitBranchSummary(p *prepared) {
	e := p.item.Entry
	if strings.TrimSpace(e.Summary) == "" {
		return
	}
	row := c.chainRow(p.time)
	row["type"] = "user"
	row["message"] = map[string]any{
		"role":    "user",
		"content": "[Summary of an abandoned Pi branch]\n\n" + e.Summary,
	}
	row["isMeta"] = true
	c.rows = append(c.rows, row)
}

func (c *converter) emitCompaction(p *prepared) {
	e := p.item.Entry
	if strings.TrimSpace(e.Summary) == "" {
		return
	}
	c.stats.Compactions++
	boundary := c.chainRow(p.time)
	boundary["type"] = "system"
	boundary["subtype"] = "compact_boundary"
	boundary["level"] = "info"
	boundary["compactMetadata"] = map[string]any{
		"trigger":    "auto",
		"preTokens":  e.TokensBefore,
		"postTokens": 0,
		"durationMs": 0,
	}
	c.rows = append(c.rows, boundary)

	summary := c.chainRow(p.time)
	summary["type"] = "user"
	summary["message"] = map[string]any{
		"role":    "user",
		"content": "This session is being continued from a previous conversation. Summary of the conversation so far:\n\n" + e.Summary,
	}
	summary["isCompactSummary"] = true
	summary["isVisibleInTranscriptOnly"] = true
	c.rows = append(c.rows, summary)
}

func (c *converter) chainRow(t time.Time) map[string]any {
	u := id.V5(id.Namespace, fmt.Sprintf("%s:row:%d", c.sessionID, len(c.rows)))
	row := map[string]any{
		"uuid":        u,
		"parentUuid":  nil,
		"timestamp":   formatTime(t),
		"sessionId":   c.sessionID,
		"session_id":  c.sessionID,
		"cwd":         c.cwd,
		"gitBranch":   c.opts.GitBranch,
		"version":     c.opts.CCVersion,
		"userType":    "external",
		"entrypoint":  "cli",
		"isSidechain": false,
	}
	if c.lastUUID != "" {
		row["parentUuid"] = c.lastUUID
	}
	c.lastUUID = u
	return row
}

func (c *converter) modelFor(model string) string {
	m := strings.TrimSpace(model)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	if strings.HasPrefix(strings.ToLower(m), "claude-") {
		return m
	}
	if c.opts.Model != "" {
		if !c.seenModel[model] {
			c.seenModel[model] = true
			if model != "" {
				c.warn("mapped non-Anthropic model %q to %q", model, c.opts.Model)
			}
		}
		return c.opts.Model
	}
	return "claude-sonnet-5-5"
}

func (c *converter) assistantBlocks(raw json.RawMessage) ([]map[string]any, error) {
	text, isString, blocks, err := pi.DecodeContent(raw)
	if err != nil {
		return nil, err
	}
	if isString {
		if strings.TrimSpace(text) == "" {
			return nil, nil
		}
		return []map[string]any{{"type": "text", "text": text}}, nil
	}
	var out []map[string]any
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if strings.TrimSpace(b.Text) == "" {
				continue
			}
			out = append(out, map[string]any{"type": "text", "text": b.Text})
		case "thinking":
			switch c.opts.Thinking {
			case "drop":
				c.stats.ThinkingDropped++
			case "text":
				if b.Thinking != "" {
					out = append(out, map[string]any{"type": "text", "text": "[reasoning]\n" + b.Thinking})
					c.stats.ThinkingDropped++
				}
			default: // signed
				if c.provider != "anthropic" || !validSignature(b.ThinkingSignature) {
					c.stats.ThinkingDropped++
					continue
				}
				if b.Thinking == "" && !b.Redacted {
					c.stats.ThinkingDropped++
					continue
				}
				if b.Redacted {
					out = append(out, map[string]any{"type": "redacted_thinking", "data": b.ThinkingSignature})
				} else {
					out = append(out, map[string]any{
						"type":      "thinking",
						"thinking":  b.Thinking,
						"signature": b.ThinkingSignature,
					})
				}
				c.stats.ThinkingKept++
			}
		case "toolCall":
			if b.ID == "" {
				c.warn("dropped tool call with no id")
				continue
			}
			name, input := mapToolCall(b.Name, b.Arguments, c.opts.ToolMap)
			out = append(out, map[string]any{
				"type":  "tool_use",
				"id":    b.ID,
				"name":  name,
				"input": input,
			})
		case "image":
			// Assistant image blocks are not valid Anthropic input. Pi rarely
			// records them; keep a text marker rather than failing the chain.
			c.warn("skipped image block in assistant message")
		default:
			c.warn("skipped unknown assistant block type %q", b.Type)
		}
	}
	return out, nil
}

func (c *converter) firstUserPrompt(items []*prepared) string {
	for _, p := range items {
		if p.kind == "message" && p.role == "user" {
			content, ok := userContent(p.content)
			if !ok {
				continue
			}
			if s := contentText(content); s != "" {
				return s
			}
		}
	}
	return ""
}

func (c *converter) warn(format string, args ...any) {
	c.warnings = append(c.warnings, fmt.Sprintf(format, args...))
}

func dedupe(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func entryTime(e *pi.Entry, fallback time.Time) time.Time {
	if t, ok := parseTime(e.Timestamp); ok {
		return t
	}
	if e.Message != nil && e.Message.Timestamp > 0 {
		return time.UnixMilli(e.Message.Timestamp)
	}
	if !fallback.IsZero() {
		return fallback
	}
	return time.Unix(0, 0).UTC()
}

func countImages(raw json.RawMessage) int {
	_, _, blocks, err := pi.DecodeContent(raw)
	if err != nil {
		return 0
	}
	n := 0
	for _, b := range blocks {
		if b.Type == "image" {
			n++
		}
	}
	return n
}

func parseTime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000Z"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func formatTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

func claudeUsage(u *pi.Usage) map[string]any {
	if u == nil {
		u = &pi.Usage{}
	}
	usage := map[string]any{
		"input_tokens":                u.Input,
		"output_tokens":               u.Output,
		"cache_creation_input_tokens": u.CacheWrite,
		"cache_read_input_tokens":     u.CacheRead,
		"service_tier":                "standard",
	}
	if u.Reasoning > 0 {
		usage["output_tokens_details"] = map[string]any{"thinking_tokens": u.Reasoning}
	}
	if u.CacheWrite > 0 || u.CacheWrite1h > 0 {
		usage["cache_creation"] = map[string]any{
			"ephemeral_5m_input_tokens": u.CacheWrite,
			"ephemeral_1h_input_tokens": u.CacheWrite1h,
		}
	}
	return usage
}

func stopReason(s string) string {
	switch s {
	case "toolUse", "tool_use":
		return "tool_use"
	case "length", "max_tokens":
		return "max_tokens"
	case "stop", "end_turn", "":
		return "end_turn"
	default:
		return "end_turn"
	}
}

func validSignature(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[") {
		return false
	}
	return true
}

func userContent(raw json.RawMessage) (any, bool) {
	text, isString, blocks, err := pi.DecodeContent(raw)
	if err != nil {
		return nil, false
	}
	if isString {
		if strings.TrimSpace(text) == "" {
			return nil, false
		}
		return text, true
	}
	var parts []any
	var texts []string
	hasImage := false
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if strings.TrimSpace(b.Text) == "" {
				continue
			}
			texts = append(texts, b.Text)
			parts = append(parts, map[string]any{"type": "text", "text": b.Text})
		case "image":
			hasImage = true
			parts = append(parts, imageBlock(b))
		default:
			// Ignore non-user block types.
		}
	}
	if len(parts) == 0 {
		return nil, false
	}
	if !hasImage {
		return strings.Join(texts, "\n\n"), true
	}
	return parts, true
}

func contentText(content any) string {
	switch v := content.(type) {
	case string:
		return strings.TrimSpace(v)
	case []any:
		var parts []string
		for _, p := range v {
			if m, ok := p.(map[string]any); ok {
				if t, ok := m["text"].(string); ok && t != "" {
					parts = append(parts, t)
				}
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n\n"))
	}
	return ""
}

func toolResultContent(raw json.RawMessage) []any {
	text, isString, blocks, err := pi.DecodeContent(raw)
	if err != nil {
		return []any{map[string]any{"type": "text", "text": "(unreadable tool result)"}}
	}
	if isString {
		if text == "" {
			text = "(no output)"
		}
		return []any{map[string]any{"type": "text", "text": text}}
	}
	var out []any
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if strings.TrimSpace(b.Text) != "" {
				out = append(out, map[string]any{"type": "text", "text": b.Text})
			}
		case "image":
			out = append(out, imageBlock(b))
		}
	}
	if len(out) == 0 {
		out = append(out, map[string]any{"type": "text", "text": "(no output)"})
	}
	return out
}

func imageBlock(b pi.Block) map[string]any {
	return map[string]any{
		"type": "image",
		"source": map[string]any{
			"type":       "base64",
			"media_type": b.MimeType,
			"data":       b.Data,
		},
	}
}

func (c *converter) toolUseRow(toolCallID, rowUUID string) {
	if c.toolUseRows == nil {
		c.toolUseRows = make(map[string]string)
	}
	c.toolUseRows[toolCallID] = rowUUID
}

func (c *converter) toolUseSource(toolCallID string) string {
	return c.toolUseRows[toolCallID]
}
