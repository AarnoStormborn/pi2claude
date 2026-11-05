// Package pi reads Pi coding-agent session files and reconstructs the context
// that Pi would send to a model.
package pi

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SessionHeader is the first JSONL record of a Pi session.
type SessionHeader struct {
	Type          string  `json:"type"`
	Version       int     `json:"version"`
	ID            string  `json:"id"`
	Timestamp     string  `json:"timestamp"`
	CWD           string  `json:"cwd"`
	ParentSession *string `json:"parentSession,omitempty"`
}

// Usage is Pi's token accounting.
type Usage struct {
	Input        int `json:"input"`
	Output       int `json:"output"`
	CacheRead    int `json:"cacheRead"`
	CacheWrite   int `json:"cacheWrite"`
	CacheWrite1h int `json:"cacheWrite1h"`
	Reasoning    int `json:"reasoning"`
	TotalTokens  int `json:"totalTokens"`
	Cost         any `json:"cost"`
}

// Block is one content block. Fields are a union of Pi's block types.
type Block struct {
	Type              string          `json:"type"`
	Text              string          `json:"text,omitempty"`
	TextSignature     string          `json:"textSignature,omitempty"`
	Thinking          string          `json:"thinking,omitempty"`
	ThinkingSignature string          `json:"thinkingSignature,omitempty"`
	Redacted          bool            `json:"redacted,omitempty"`
	Data              string          `json:"data,omitempty"`
	MimeType          string          `json:"mimeType,omitempty"`
	ID                string          `json:"id,omitempty"`
	Name              string          `json:"name,omitempty"`
	Namespace         string          `json:"namespace,omitempty"`
	Arguments         json.RawMessage `json:"arguments,omitempty"`
}

// Message is a Pi AgentMessage. Fields not used by a role are zero.
type Message struct {
	Role string `json:"role"`

	Content   json.RawMessage `json:"content,omitempty"`
	Timestamp int64           `json:"timestamp,omitempty"`

	// toolResult
	ToolCallID string          `json:"toolCallId,omitempty"`
	ToolName   string          `json:"toolName,omitempty"`
	IsError    bool            `json:"isError,omitempty"`
	Details    json.RawMessage `json:"details,omitempty"`

	// bashExecution
	Command        string `json:"command,omitempty"`
	Output         string `json:"output,omitempty"`
	ExitCode       *int   `json:"exitCode,omitempty"`
	Cancelled      bool   `json:"cancelled,omitempty"`
	Truncated      bool   `json:"truncated,omitempty"`
	FullOutputPath string `json:"fullOutputPath,omitempty"`
	ExcludeFromCtx bool   `json:"excludeFromContext,omitempty"`

	// assistant
	API          string `json:"api,omitempty"`
	Provider     string `json:"provider,omitempty"`
	Model        string `json:"model,omitempty"`
	ResponseID   string `json:"responseId,omitempty"`
	StopReason   string `json:"stopReason,omitempty"`
	Usage        *Usage `json:"usage,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`

	// custom
	CustomType string          `json:"customType,omitempty"`
	Display    json.RawMessage `json:"display,omitempty"`
}

// Entry is one session JSONL record. Session entries form a tree via ID/ParentID.
type Entry struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	ParentID  *string         `json:"parentId"`
	Timestamp string          `json:"timestamp"`
	Message   *Message        `json:"message,omitempty"`
	Raw       json.RawMessage `json:"-"`

	// compaction
	Summary          string          `json:"summary,omitempty"`
	FirstKeptEntryID string          `json:"firstKeptEntryId,omitempty"`
	TokensBefore     int             `json:"tokensBefore,omitempty"`
	SystemMessage    json.RawMessage `json:"systemMessage,omitempty"`
	Details          json.RawMessage `json:"details,omitempty"`

	// context_edit
	TargetID    string          `json:"targetId,omitempty"`
	Replacement json.RawMessage `json:"replacement,omitempty"`

	// custom / custom_message
	CustomType string          `json:"customType,omitempty"`
	Content    json.RawMessage `json:"content,omitempty"`
	Display    json.RawMessage `json:"display,omitempty"`
	Data       json.RawMessage `json:"data,omitempty"`

	// branch_summary
	FromID string `json:"fromId,omitempty"`

	// metadata
	Name          string `json:"name,omitempty"`
	Provider      string `json:"provider,omitempty"`
	ModelID       string `json:"modelId,omitempty"`
	ThinkingLevel string `json:"thinkingLevel,omitempty"`
	Kind          string `json:"kind,omitempty"`
	Usage         *Usage `json:"usage,omitempty"`
}

// Session is a parsed Pi session file.
type Session struct {
	Path     string
	Header   SessionHeader
	Entries  []*Entry
	ByID     map[string]*Entry
	Warnings []string
	Size     int64
	ModTime  time.Time
	SHA256   string
}

// Context is the active branch after Pi's leaf, compaction and context-edit rules.
type Context struct {
	Items      []*Selected
	LeafID     string
	Compaction *Entry
	Title      string
	Warnings   []string
}

// Selected is one context-producing entry, optionally rewritten by a context edit.
type Selected struct {
	Entry   *Entry
	Omit    bool
	Content json.RawMessage // non-nil replaces the entry's content
}

// Load reads and parses a Pi session file.
func Load(path string) (*Session, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return nil, err
	}

	// Some Pi tool results are tens of megabytes on a single line.
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 256*1024*1024)

	s := &Session{
		Path:    path,
		ByID:    make(map[string]*Entry),
		Size:    st.Size(),
		ModTime: st.ModTime(),
	}
	line := 0
	hash := sha256.New()
	for sc.Scan() {
		line++
		b := sc.Bytes()
		if len(strings.TrimSpace(string(b))) == 0 {
			continue
		}
		_, _ = hash.Write(b)
		_, _ = hash.Write([]byte{'\n'})
		var raw json.RawMessage
		if err := json.Unmarshal(b, &raw); err != nil {
			s.Warnings = append(s.Warnings, fmt.Sprintf("line %d: invalid JSON: %v", line, err))
			continue
		}
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(b, &probe); err != nil {
			s.Warnings = append(s.Warnings, fmt.Sprintf("line %d: invalid record: %v", line, err))
			continue
		}
		if line == 1 || s.Header.ID == "" {
			var h SessionHeader
			if err := json.Unmarshal(b, &h); err == nil && h.Type == "session" {
				s.Header = h
				continue
			}
			if line == 1 {
				return nil, fmt.Errorf("%s: first line is not a Pi session header", path)
			}
		}
		var e Entry
		if err := json.Unmarshal(b, &e); err != nil {
			s.Warnings = append(s.Warnings, fmt.Sprintf("line %d: %v", line, err))
			continue
		}
		e.Raw = raw
		s.Entries = append(s.Entries, &e)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if s.Header.ID == "" {
		return nil, fmt.Errorf("%s: missing Pi session header", path)
	}
	s.SHA256 = hex.EncodeToString(hash.Sum(nil))
	s.normalizeLegacy()
	for _, e := range s.Entries {
		if e.ID == "" {
			s.Warnings = append(s.Warnings, fmt.Sprintf("entry without id at type %q ignored", e.Type))
			continue
		}
		if _, dup := s.ByID[e.ID]; dup {
			s.Warnings = append(s.Warnings, fmt.Sprintf("duplicate entry id %q", e.ID))
			continue
		}
		s.ByID[e.ID] = e
	}
	return s, nil
}

// normalizeLegacy makes version-1 linear sessions look like version-2 trees.
func (s *Session) normalizeLegacy() {
	if s.Header.Version >= 2 {
		return
	}
	var prev *string
	for i, e := range s.Entries {
		if e.ID == "" {
			e.ID = fmt.Sprintf("legacy-%d", i)
		}
		if e.ParentID == nil && e.Type != "session" {
			e.ParentID = prev
		}
		id := e.ID
		prev = &id
	}
	if s.Header.Version == 0 {
		s.Header.Version = 1
	}
}

// EntryByID returns an entry by id.
func (s *Session) EntryByID(id string) *Entry {
	if s.ByID == nil {
		s.ByID = make(map[string]*Entry)
		for _, e := range s.Entries {
			if e.ID != "" {
				s.ByID[e.ID] = e
			}
		}
	}
	return s.ByID[id]
}

// Leaf returns the current leaf, which Pi defines as the last entry in the file.
func (s *Session) Leaf() *Entry {
	for i := len(s.Entries) - 1; i >= 0; i-- {
		if s.Entries[i].ID != "" {
			return s.Entries[i]
		}
	}
	return nil
}

// Title returns the last session_info name.
func (s *Session) Title() string {
	name := ""
	for _, e := range s.Entries {
		if e.Type == "session_info" && strings.TrimSpace(e.Name) != "" {
			name = strings.TrimSpace(e.Name)
		}
	}
	return name
}

// BuildContext follows Pi's buildContextEntries + context-edit projection.
func (s *Session) BuildContext() (*Context, error) {
	leaf := s.Leaf()
	if leaf == nil {
		return nil, fmt.Errorf("session has no entries")
	}
	c := &Context{LeafID: leaf.ID, Title: s.Title()}
	c.Warnings = append(c.Warnings, s.Warnings...)

	path := make([]*Entry, 0, 64)
	seen := make(map[string]bool)
	for id := leaf.ID; id != ""; {
		if seen[id] {
			c.Warnings = append(c.Warnings, fmt.Sprintf("cycle in parent chain at %q", id))
			break
		}
		seen[id] = true
		e := s.EntryByID(id)
		if e == nil {
			c.Warnings = append(c.Warnings, fmt.Sprintf("parent entry %q not found", id))
			break
		}
		path = append(path, e)
		if e.ParentID == nil || *e.ParentID == "" {
			break
		}
		id = *e.ParentID
	}
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}

	// Latest compaction on the active branch.
	var compaction *Entry
	for _, e := range path {
		if e.Type == "compaction" {
			compaction = e
		}
	}
	c.Compaction = compaction

	var selected []*Entry
	if compaction == nil {
		selected = path
	} else {
		ci := indexOf(path, compaction)
		selected = append(selected, compaction)
		found := false
		for i := 0; i < ci; i++ {
			e := path[i]
			if e.ID == compaction.FirstKeptEntryID {
				found = true
			}
			if found && !(e.Type == "message" && e.Message != nil && e.Message.Role == "system") {
				selected = append(selected, e)
			}
		}
		if !found && compaction.FirstKeptEntryID != "" && compaction.FirstKeptEntryID != compaction.ID {
			c.Warnings = append(c.Warnings, fmt.Sprintf("compaction firstKeptEntryId %q not found on active branch", compaction.FirstKeptEntryID))
		}
		selected = append(selected, path[ci+1:]...)
	}

	// Latest context edit per target on the active branch wins.
	edits := make(map[string]*Entry)
	for _, e := range path {
		if e.Type == "context_edit" && e.TargetID != "" {
			edits[e.TargetID] = e
		}
	}

	for _, e := range selected {
		switch e.Type {
		case "message", "custom_message", "branch_summary", "compaction":
		default:
			continue
		}
		item := &Selected{Entry: e}
		if edit := edits[e.ID]; edit != nil {
			omit, content, err := ParseReplacement(edit.Replacement)
			if err != nil {
				c.Warnings = append(c.Warnings, fmt.Sprintf("context_edit %q: %v", edit.ID, err))
			} else if omit {
				item.Omit = true
			} else {
				item.Content = content
			}
		}
		c.Items = append(c.Items, item)
	}
	return c, nil
}

func indexOf(path []*Entry, target *Entry) int {
	for i, e := range path {
		if e == target || e.ID == target.ID {
			return i
		}
	}
	return -1
}

// ParseReplacement parses a context_edit replacement. A null replacement omits
// the target. Otherwise the content may be wrapped in {"content": ...} or be
// the content value itself.
func ParseReplacement(raw json.RawMessage) (omit bool, content json.RawMessage, err error) {
	t := strings.TrimSpace(string(raw))
	if t == "" || t == "null" {
		return true, nil, nil
	}
	if strings.HasPrefix(t, "{") {
		var w struct {
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(raw, &w); err != nil {
			return false, nil, err
		}
		if w.Content == nil {
			return false, nil, fmt.Errorf("replacement object has no content")
		}
		return false, w.Content, nil
	}
	return false, raw, nil
}

// DecodeContent decodes Pi content, which may be a string or an array of
// content blocks. It returns the string form when content is a string.
func DecodeContent(raw json.RawMessage) (text string, isString bool, blocks []Block, err error) {
	t := strings.TrimSpace(string(raw))
	if t == "" || t == "null" {
		return "", false, nil, nil
	}
	switch t[0] {
	case '"':
		if err := json.Unmarshal(raw, &text); err != nil {
			return "", false, nil, err
		}
		return text, true, nil, nil
	case '[':
		if err := json.Unmarshal(raw, &blocks); err != nil {
			return "", false, nil, err
		}
		return "", false, blocks, nil
	case '{':
		var b Block
		if err := json.Unmarshal(raw, &b); err != nil {
			return "", false, nil, err
		}
		return "", false, []Block{b}, nil
	default:
		return "", false, nil, fmt.Errorf("unexpected content JSON: %.40s", t)
	}
}

// EntryContent returns an entry's overridable content (message content for
// messages, content for custom messages).
func (sel *Selected) EntryContent() json.RawMessage {
	if sel.Content != nil {
		return sel.Content
	}
	e := sel.Entry
	if e.Type == "message" && e.Message != nil {
		return e.Message.Content
	}
	if e.Type == "custom_message" {
		return e.Content
	}
	return nil
}

// Messages returns the active, context-projected Pi messages in order. It is
// primarily useful for tests and inspection; the converter uses Items directly.
func (c *Context) Messages() []*Message {
	var out []*Message
	for _, item := range c.Items {
		if item.Omit || item.Entry.Type != "message" || item.Entry.Message == nil {
			continue
		}
		m := *item.Entry.Message
		if item.Content != nil {
			m.Content = item.Content
		}
		out = append(out, &m)
	}
	return out
}

// SortedFiles returns Pi session files under root sorted by path.
func SortedFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(d.Name(), ".jsonl") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}
