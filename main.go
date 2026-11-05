package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"pi2claude/internal/claude"
	"pi2claude/internal/convert"
	"pi2claude/internal/id"
	"pi2claude/internal/pi"
	"pi2claude/internal/store"
	"pi2claude/internal/validate"
)

const version = "0.1.0"

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]
	var err error
	switch cmd {
	case "list", "ls":
		err = cmdList(args)
	case "migrate":
		err = cmdMigrate(args)
	case "inspect":
		err = cmdInspect(args)
	case "validate":
		err = cmdValidate(args)
	case "version", "--version", "-v":
		fmt.Printf("pi2claude %s\n", version)
	case "help", "--help", "-h":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`pi2claude - migrate Pi coding-agent sessions into Claude Code

Usage:
  pi2claude list    [flags]
  pi2claude migrate --all [flags]
  pi2claude migrate --session <id|path> ... [flags]
  pi2claude inspect <id|path> [flags]
  pi2claude validate <transcript.jsonl> [--json]
  pi2claude version

Migrate flags:
  --pi-dir <dir>        Pi sessions directory (default ~/.pi/agent/sessions)
  --claude-dir <dir>    Claude config directory (default $CLAUDE_CONFIG_DIR or ~/.claude)
  --all                 migrate every Pi session
  --session <id|path>   migrate one session (repeatable)
  --project <text>      migrate sessions whose cwd/project folder contains text
  --dry-run             show what would happen; write nothing
  --force               overwrite existing targets (backs up by default)
  --no-backup           do not create .bak files when overwriting
  --model <id>          Claude model for non-Anthropic history (default claude-sonnet-5-5)
  --thinking <mode>     signed (default), drop, or text
  --cc-version <ver>    version stamped on transcript rows (default: detect claude)
  --cwd <dir>           override every session's cwd
  --missing-cwd <mode>  keep (default), skip, current, or remap:<dir>
  --no-tool-map         keep Pi tool names instead of mapping to Claude names
  --json                machine-readable output
  --verbose             show warnings and details
  --quiet               only print the summary

By default pi2claude follows Pi's active leaf, applies the latest compaction
and context edits, and writes a context-faithful Claude Code transcript.
`)
}

func cmdList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	piDir := fs.String("pi-dir", "", "Pi sessions directory")
	claudeDir := fs.String("claude-dir", "", "Claude config directory")
	sessions := &stringList{}
	fs.Var(sessions, "session", "filter by session id, filename, or path (repeatable)")
	project := fs.String("project", "", "filter by cwd/project folder substring")
	asJSON := fs.Bool("json", false, "JSON output")
	verbose := fs.Bool("verbose", false, "show extra detail")
	if err := fs.Parse(args); err != nil {
		return err
	}
	piRoot, err := resolvePiDir(*piDir)
	if err != nil {
		return err
	}
	cRoot, err := resolveClaudeDir(*claudeDir)
	if err != nil {
		return err
	}
	m, err := store.LoadManifest(store.ManifestPath(cRoot))
	if err != nil {
		return err
	}
	files, err := pi.SortedFiles(piRoot)
	if err != nil {
		return err
	}
	var out []listRow
	for _, f := range files {
		s, err := pi.Load(f)
		if err != nil {
			out = append(out, listRow{PiPath: f, Status: "invalid", Error: err.Error()})
			continue
		}
		if !matchesFilters(s, f, *sessions, *project) {
			continue
		}
		row := listRow{
			PiSessionID: s.Header.ID,
			PiPath:      f,
			CWD:         s.Header.CWD,
			Size:        s.Size,
			Messages:    countMessages(s),
			Title:       s.Title(),
		}
		cwd, _, cwdErr := resolveCWD(s, "", cwdMissingKeep)
		if cwdErr != nil {
			row.Status = "missing-cwd"
			row.Error = cwdErr.Error()
			out = append(out, row)
			continue
		}
		row.CWD = cwd
		target := claude.TranscriptPath(cRoot, cwd, choosePreviewID(s, m, cRoot, cwd))
		row.ClaudePath = target
		me, has := m.Sessions[s.Header.ID]
		switch {
		case !fileExists(target):
			row.Status = "new"
		case has && me.ClaudePath == target:
			if me.PiSize == s.Size && me.PiMTime == s.ModTime.UnixMilli() {
				row.Status = "up-to-date"
			} else {
				row.Status = "stale"
			}
		case store.HasMarker(target, s.Header.ID):
			row.Status = "stale"
		default:
			row.Status = "foreign"
		}
		out = append(out, row)
	}
	if *asJSON {
		return printJSON(out)
	}
	printList(out, *verbose)
	return nil
}

const cwdMissingKeep = "keep"

type listRow struct {
	PiSessionID string `json:"piSessionId"`
	PiPath      string `json:"piPath"`
	CWD         string `json:"cwd"`
	ClaudePath  string `json:"claudePath,omitempty"`
	Status      string `json:"status"`
	Size        int64  `json:"size"`
	Messages    int    `json:"messages"`
	Title       string `json:"title,omitempty"`
	Error       string `json:"error,omitempty"`
}

func printList(rows []listRow, verbose bool) {
	if len(rows) == 0 {
		fmt.Println("no sessions")
		return
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].PiPath < rows[j].PiPath })
	for _, r := range rows {
		title := r.Title
		if !verbose && len(title) > 40 {
			title = title[:39] + "…"
		}
		fmt.Printf("%-12s %s  %5d msgs  %8s  %s", r.Status, shortID(r.PiSessionID), r.Messages, humanBytes(r.Size), r.CWD)
		if title != "" {
			fmt.Printf("  %q", title)
		}
		fmt.Println()
		if r.Status == "invalid" || r.Status == "missing-cwd" {
			fmt.Printf("  %s\n", r.Error)
		}
	}
}

func cmdMigrate(args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	piDir := fs.String("pi-dir", "", "Pi sessions directory")
	claudeDir := fs.String("claude-dir", "", "Claude config directory")
	all := fs.Bool("all", false, "migrate all sessions")
	sessions := &stringList{}
	fs.Var(sessions, "session", "session id, filename, or path (repeatable)")
	project := fs.String("project", "", "cwd/project folder substring")
	dry := fs.Bool("dry-run", false, "write nothing")
	force := fs.Bool("force", false, "overwrite existing targets")
	noBackup := fs.Bool("no-backup", false, "do not create backups")
	model := fs.String("model", envDefault("PI2CLAUDE_MODEL", "claude-sonnet-5-5"), "Claude model for non-Anthropic history")
	thinking := fs.String("thinking", "signed", "signed, drop, or text")
	ccVersion := fs.String("cc-version", "", "Claude Code version stamp")
	cwdOverride := fs.String("cwd", "", "override every session cwd")
	missing := fs.String("missing-cwd", "keep", "keep, skip, current, or remap:<dir>")
	toolMap := fs.Bool("tool-map", true, "map Pi tool names to Claude names")
	asJSON := fs.Bool("json", false, "JSON output")
	verbose := fs.Bool("verbose", false, "show warnings")
	quiet := fs.Bool("quiet", false, "summary only")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *thinking != "signed" && *thinking != "drop" && *thinking != "text" {
		return fmt.Errorf("--thinking must be signed, drop, or text")
	}
	if !*all && len(*sessions) == 0 && *project == "" {
		return errors.New("refusing to migrate without a selection: pass --all, --session, or --project")
	}
	piRoot, err := resolvePiDir(*piDir)
	if err != nil {
		return err
	}
	cRoot, err := resolveClaudeDir(*claudeDir)
	if err != nil {
		return err
	}
	ccv := *ccVersion
	if ccv == "" {
		ccv = detectCCVersion()
	}
	m, err := store.LoadManifest(store.ManifestPath(cRoot))
	if err != nil {
		return err
	}
	files, err := pi.SortedFiles(piRoot)
	if err != nil {
		return err
	}
	var results []migrateResult
	changedManifest := false
	gitCache := make(map[string]string)
	for _, f := range files {
		s, err := pi.Load(f)
		if err != nil {
			results = append(results, migrateResult{PiPath: f, Status: "failed", Error: err.Error()})
			continue
		}
		if !matchesFilters(s, f, *sessions, *project) {
			continue
		}
		cwd, skip, err := resolveCWD(s, *cwdOverride, *missing)
		if err != nil {
			results = append(results, resultFor(s, "failed", err.Error()))
			continue
		}
		if skip {
			results = append(results, resultFor(s, "skipped", errMissingCWD(s)))
			continue
		}
		branch := gitCache[cwd]
		if branch == "" {
			branch = detectGitBranch(cwd)
			gitCache[cwd] = branch
		}
		sid, target, err := chooseSessionID(s, m, cRoot, cwd)
		if err != nil {
			results = append(results, resultFor(s, "failed", err.Error()))
			continue
		}
		res := convert.Options{
			SessionID: sid,
			CWD:       cwd,
			CCVersion: ccv,
			Model:     *model,
			Thinking:  *thinking,
			ToolMap:   *toolMap,
			GitBranch: branch,
		}
		conv, err := convert.Convert(s, res)
		if err != nil {
			results = append(results, resultFor(s, "failed", err.Error()))
			continue
		}
		if !dirExists(cwd) {
			conv.Warnings = append(conv.Warnings, fmt.Sprintf("cwd does not exist; keeping recorded path %q", cwd))
		}
		if errs := validate.Validate(conv.Rows, conv.SessionID); len(errs) > 0 {
			results = append(results, migrateResult{
				PiSessionID: s.Header.ID,
				PiPath:      f,
				CWD:         cwd,
				ClaudePath:  target,
				Status:      "failed",
				Error:       "validation: " + joinErrors(errs),
				Warnings:    conv.Warnings,
				Stats:       conv.Stats,
			})
			continue
		}

		exists := fileExists(target)
		ours := false
		if exists {
			ours = store.HasMarker(target, s.Header.ID)
			if me, ok := m.Sessions[s.Header.ID]; ok && me.ClaudePath == target {
				ours = true
			}
		}
		upToDate := false
		if exists && ours {
			if me, ok := m.Sessions[s.Header.ID]; ok && me.PiSHA256 == s.SHA256 && me.ClaudePath == target {
				upToDate = true
			}
		}
		r := migrateResult{
			PiSessionID:     s.Header.ID,
			PiPath:          f,
			CWD:             cwd,
			ClaudeSessionID: conv.SessionID,
			ClaudePath:      target,
			Warnings:        conv.Warnings,
			Stats:           conv.Stats,
		}
		switch {
		case upToDate && !*force:
			r.Status = "up-to-date"
			results = append(results, r)
			continue
		case exists && !ours && !*force:
			r.Status = "conflict"
			r.Error = "target exists and was not created by pi2claude; pass --force to overwrite"
			results = append(results, r)
			continue
		case *dry:
			if exists && ours {
				r.Status = "would-overwrite"
			} else {
				r.Status = "would-convert"
			}
			results = append(results, r)
			continue
		}
		if exists && !*noBackup {
			bak, err := store.Backup(target)
			if err != nil {
				r.Status = "failed"
				r.Error = "backup: " + err.Error()
				results = append(results, r)
				continue
			}
			r.BackupPath = bak
		}
		if err := store.AtomicWrite(target, conv.JSONL, 0o600); err != nil {
			r.Status = "failed"
			r.Error = err.Error()
			results = append(results, r)
			continue
		}
		r.Status = "converted"
		results = append(results, r)
		m.Sessions[s.Header.ID] = store.ManifestEntry{
			PiPath:           f,
			PiSize:           s.Size,
			PiMTime:          s.ModTime.UnixMilli(),
			PiSHA256:         s.SHA256,
			ClaudeSessionID:  conv.SessionID,
			ClaudePath:       target,
			ConvertedAt:      time.Now().UTC().Format(time.RFC3339),
			ConverterVersion: version,
		}
		changedManifest = true
	}
	if changedManifest && !*dry {
		if err := store.SaveManifest(store.ManifestPath(cRoot), m); err != nil {
			return fmt.Errorf("save manifest: %w", err)
		}
	}
	if *asJSON {
		return printJSON(results)
	}
	printMigrate(results, *verbose, *quiet)
	return nil
}

type migrateResult struct {
	PiSessionID     string        `json:"piSessionId"`
	PiPath          string        `json:"piPath"`
	CWD             string        `json:"cwd,omitempty"`
	ClaudeSessionID string        `json:"claudeSessionId,omitempty"`
	ClaudePath      string        `json:"claudePath,omitempty"`
	BackupPath      string        `json:"backupPath,omitempty"`
	Status          string        `json:"status"`
	Error           string        `json:"error,omitempty"`
	Warnings        []string      `json:"warnings,omitempty"`
	Stats           convert.Stats `json:"stats,omitempty"`
}

func resultFor(s *pi.Session, status, errText string) migrateResult {
	return migrateResult{
		PiSessionID: s.Header.ID,
		PiPath:      s.Path,
		CWD:         s.Header.CWD,
		Status:      status,
		Error:       errText,
	}
}

func printMigrate(results []migrateResult, verbose, quiet bool) {
	counts := map[string]int{}
	for _, r := range results {
		counts[r.Status]++
	}
	if !quiet {
		for _, r := range results {
			mark := "✓"
			switch r.Status {
			case "converted", "would-convert":
				mark = "+"
			case "up-to-date":
				mark = "="
			case "skipped":
				mark = "-"
			case "conflict", "failed":
				mark = "!"
			}
			fmt.Printf("%s %-14s %s -> %s", mark, r.Status, shortID(r.PiSessionID), r.ClaudePath)
			if r.BackupPath != "" && verbose {
				fmt.Printf(" (backup %s)", r.BackupPath)
			}
			if r.Stats.Rows > 0 {
				fmt.Printf("  rows=%d msgs=%d tools=%d", r.Stats.Rows, r.Stats.Messages, r.Stats.ToolCalls)
			}
			if len(r.Warnings) > 0 {
				fmt.Printf("  warnings=%d", len(r.Warnings))
			}
			fmt.Println()
			if r.Error != "" {
				fmt.Printf("  %s\n", r.Error)
			}
			if verbose {
				for _, w := range r.Warnings {
					fmt.Printf("  warning: %s\n", w)
				}
			}
		}
	}
	fmt.Printf("%d converted, %d up-to-date, %d skipped, %d conflicts, %d failed, %d dry-run\n",
		counts["converted"], counts["up-to-date"], counts["skipped"], counts["conflict"], counts["failed"],
		counts["would-convert"]+counts["would-overwrite"])
}

func cmdInspect(args []string) error {
	fs := flag.NewFlagSet("inspect", flag.ExitOnError)
	piDir := fs.String("pi-dir", "", "Pi sessions directory")
	claudeDir := fs.String("claude-dir", "", "Claude config directory")
	model := fs.String("model", envDefault("PI2CLAUDE_MODEL", "claude-sonnet-5-5"), "Claude model for non-Anthropic history")
	thinking := fs.String("thinking", "signed", "signed, drop, or text")
	ccVersion := fs.String("cc-version", "", "Claude Code version stamp")
	cwdOverride := fs.String("cwd", "", "override cwd")
	missing := fs.String("missing-cwd", "keep", "keep, skip, current, or remap:<dir>")
	toolMap := fs.Bool("tool-map", true, "map Pi tool names")
	dump := fs.Bool("dump", false, "print the generated JSONL")
	asJSON := fs.Bool("json", false, "JSON output")
	arg, err := parseOnePositional(fs, args)
	if err != nil {
		return err
	}
	piRoot, err := resolvePiDir(*piDir)
	if err != nil {
		return err
	}
	cRoot, err := resolveClaudeDir(*claudeDir)
	if err != nil {
		return err
	}
	f, s, err := findOne(piRoot, arg)
	if err != nil {
		return err
	}
	cwd, skip, err := resolveCWD(s, *cwdOverride, *missing)
	if err != nil {
		return err
	}
	if skip {
		return errors.New(errMissingCWD(s))
	}
	ccv := *ccVersion
	if ccv == "" {
		ccv = detectCCVersion()
	}
	m, err := store.LoadManifest(store.ManifestPath(cRoot))
	if err != nil {
		return err
	}
	sid, target, err := chooseSessionID(s, m, cRoot, cwd)
	if err != nil {
		return err
	}
	conv, err := convert.Convert(s, convert.Options{
		SessionID: sid,
		CWD:       cwd,
		CCVersion: ccv,
		Model:     *model,
		Thinking:  *thinking,
		ToolMap:   *toolMap,
		GitBranch: detectGitBranch(cwd),
	})
	if err != nil {
		return err
	}
	if !dirExists(cwd) {
		conv.Warnings = append(conv.Warnings, fmt.Sprintf("cwd does not exist; keeping recorded path %q", cwd))
	}
	if *dump {
		_, err := os.Stdout.Write(conv.JSONL)
		return err
	}
	out := struct {
		PiPath      string           `json:"piPath"`
		PiSessionID string           `json:"piSessionId"`
		Header      pi.SessionHeader `json:"header"`
		Title       string           `json:"title,omitempty"`
		Leaf        string           `json:"leaf,omitempty"`
		CWD         string           `json:"cwd"`
		ClaudePath  string           `json:"claudePath"`
		SessionID   string           `json:"claudeSessionId"`
		Stats       convert.Stats    `json:"stats"`
		Warnings    []string         `json:"warnings,omitempty"`
	}{
		PiPath: f, PiSessionID: s.Header.ID, Header: s.Header, Title: conv.Title,
		CWD: cwd, ClaudePath: target, SessionID: conv.SessionID, Stats: conv.Stats, Warnings: conv.Warnings,
	}
	if leaf := s.Leaf(); leaf != nil {
		out.Leaf = leaf.ID
	}
	if *asJSON {
		return printJSON(out)
	}
	fmt.Printf("Pi session:   %s\n", s.Header.ID)
	fmt.Printf("Pi file:      %s\n", f)
	fmt.Printf("cwd:          %s\n", cwd)
	fmt.Printf("leaf:         %s\n", out.Leaf)
	if conv.Title != "" {
		fmt.Printf("title:        %s\n", conv.Title)
	}
	fmt.Printf("Claude id:    %s\n", conv.SessionID)
	fmt.Printf("Claude path:  %s\n", target)
	fmt.Printf("rows:         %d (%s)\n", conv.Stats.Rows, humanBytes(int64(conv.Stats.Bytes)))
	fmt.Printf("messages:     %d (%d user, %d assistant, %d tool results)\n", conv.Stats.Messages, conv.Stats.UserMessages, conv.Stats.AssistantMsgs, conv.Stats.ToolResults)
	fmt.Printf("tools:        %d kept, %d dropped calls, %d dropped results\n", conv.Stats.ToolCalls-conv.Stats.ToolCallsDropped, conv.Stats.ToolCallsDropped, conv.Stats.ToolResultsDropped)
	fmt.Printf("thinking:     %d kept, %d dropped\n", conv.Stats.ThinkingKept, conv.Stats.ThinkingDropped)
	fmt.Printf("compactions:  %d\n", conv.Stats.Compactions)
	for _, w := range conv.Warnings {
		fmt.Printf("warning:      %s\n", w)
	}
	return nil
}

func cmdValidate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "JSON output")
	arg, err := parseOnePositional(fs, args)
	if err != nil {
		return err
	}
	rows, errs := validate.File(arg)
	if *asJSON {
		out := struct {
			Path   string   `json:"path"`
			Rows   int      `json:"rows"`
			Errors []string `json:"errors"`
		}{Path: arg, Rows: len(rows), Errors: errorStrings(errs)}
		_ = printJSON(out)
	} else {
		fmt.Printf("%s: %d rows, %d errors\n", arg, len(rows), len(errs))
		for _, e := range errs {
			fmt.Printf("  %v\n", e)
		}
	}
	if len(errs) > 0 {
		return errors.New("validation failed")
	}
	return nil
}

func choosePreviewID(s *pi.Session, m *store.Manifest, cRoot, cwd string) string {
	sid, _, err := chooseSessionID(s, m, cRoot, cwd)
	if err != nil {
		return previewID(s.Header.ID)
	}
	return sid
}

func chooseSessionID(s *pi.Session, m *store.Manifest, cRoot, cwd string) (string, string, error) {
	dir := claude.ProjectDir(cRoot, cwd)
	piID := s.Header.ID
	if me, ok := m.Sessions[piID]; ok && me.ClaudeSessionID != "" {
		if me.ClaudePath == "" || filepath.Dir(me.ClaudePath) == dir {
			return me.ClaudeSessionID, filepath.Join(dir, me.ClaudeSessionID+".jsonl"), nil
		}
	}
	candidates := []string{previewID(piID)}
	if id.Valid(piID) && strings.ToLower(piID) != candidates[0] {
		candidates = []string{strings.ToLower(piID), candidates[0]}
	}
	for _, c := range candidates {
		p := filepath.Join(dir, c+".jsonl")
		if !fileExists(p) || store.HasMarker(p, piID) || manifestMatches(m, piID, p) {
			return c, p, nil
		}
	}
	c := id.V5(id.Namespace, "claude-session-collision:"+piID)
	p := filepath.Join(dir, c+".jsonl")
	if !fileExists(p) || store.HasMarker(p, piID) || manifestMatches(m, piID, p) {
		return c, p, nil
	}
	return "", "", fmt.Errorf("both %s and %s exist and were not created by pi2claude", filepath.Join(dir, candidates[0]+".jsonl"), p)
}

func previewID(piID string) string {
	if id.Valid(piID) {
		return strings.ToLower(piID)
	}
	return id.V5(id.Namespace, "claude-session:"+piID)
}

func manifestMatches(m *store.Manifest, piID, path string) bool {
	me, ok := m.Sessions[piID]
	return ok && me.ClaudePath == path
}

func resolveCWD(s *pi.Session, override, missing string) (string, bool, error) {
	if override != "" {
		p := claude.CanonicalPath(override)
		if !dirExists(p) {
			return p, false, fmt.Errorf("cwd override does not exist: %s", p)
		}
		return p, false, nil
	}
	raw := strings.TrimSpace(s.Header.CWD)
	if raw == "" {
		return applyMissing("", missing, s)
	}
	p := claude.CanonicalPath(raw)
	if dirExists(p) {
		return p, false, nil
	}
	return applyMissing(p, missing, s)
}

func applyMissing(p, mode string, s *pi.Session) (string, bool, error) {
	switch {
	case mode == "" || mode == "keep":
		if p == "" {
			wd, _ := os.Getwd()
			p = claude.CanonicalPath(wd)
		}
		return p, false, nil
	case mode == "skip":
		return p, true, nil
	case mode == "current":
		wd, _ := os.Getwd()
		return claude.CanonicalPath(wd), false, nil
	case strings.HasPrefix(mode, "remap:"):
		dst := strings.TrimPrefix(mode, "remap:")
		if dst == "" {
			return "", true, fmt.Errorf("empty remap directory")
		}
		p := claude.CanonicalPath(dst)
		if !dirExists(p) {
			return p, true, fmt.Errorf("remap directory does not exist: %s", p)
		}
		return p, false, nil
	default:
		return p, true, fmt.Errorf("unknown --missing-cwd mode %q", mode)
	}
}

func errMissingCWD(s *pi.Session) string {
	return fmt.Sprintf("session cwd does not exist: %q", s.Header.CWD)
}

func matchesFilters(s *pi.Session, path string, sessions []string, project string) bool {
	if project != "" {
		p := strings.ToLower(project)
		if !strings.Contains(strings.ToLower(s.Header.CWD), p) &&
			!strings.Contains(strings.ToLower(filepath.Dir(path)), p) {
			return false
		}
	}
	if len(sessions) == 0 {
		return true
	}
	base := filepath.Base(path)
	for _, q := range sessions {
		if q == "" {
			continue
		}
		if q == s.Header.ID || q == base || q == path || strings.HasSuffix(path, q) {
			return true
		}
		if strings.Contains(s.Header.ID, q) || strings.Contains(base, q) {
			return true
		}
	}
	return false
}

func findOne(piRoot, query string) (string, *pi.Session, error) {
	files, err := pi.SortedFiles(piRoot)
	if err != nil {
		return "", nil, err
	}
	var matches []string
	for _, f := range files {
		if f == query || filepath.Base(f) == query || strings.HasSuffix(f, query) {
			matches = append(matches, f)
			continue
		}
		s, err := pi.Load(f)
		if err != nil {
			continue
		}
		if s.Header.ID == query {
			matches = append(matches, f)
		}
	}
	if len(matches) == 0 {
		return "", nil, fmt.Errorf("no Pi session matches %q", query)
	}
	if len(matches) > 1 {
		return "", nil, fmt.Errorf("%q matches %d sessions; use a full path", query, len(matches))
	}
	s, err := pi.Load(matches[0])
	if err != nil {
		return "", nil, err
	}
	return matches[0], s, nil
}

func countMessages(s *pi.Session) int {
	n := 0
	for _, e := range s.Entries {
		if e.Type == "message" {
			n++
		}
	}
	return n
}

func resolvePiDir(v string) (string, error) {
	if v == "" {
		v = os.Getenv("PI2CLAUDE_PI_DIR")
	}
	if v == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		v = filepath.Join(home, ".pi", "agent", "sessions")
	}
	v = expandHome(v)
	if !dirExists(v) {
		return "", fmt.Errorf("Pi sessions directory not found: %s", v)
	}
	return v, nil
}

func resolveClaudeDir(v string) (string, error) {
	if v == "" {
		v = os.Getenv("CLAUDE_CONFIG_DIR")
	}
	if v == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		v = filepath.Join(home, ".claude")
	}
	v = expandHome(v)
	return v, nil
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

func detectCCVersion() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "claude", "--version").Output()
	if err == nil {
		re := regexp.MustCompile(`\d+\.\d+\.\d+`)
		if m := re.FindString(string(out)); m != "" {
			return m
		}
	}
	return "2.1.318"
}

func detectGitBranch(cwd string) string {
	if cwd == "" || !dirExists(cwd) {
		return "HEAD"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", cwd, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return "HEAD"
	}
	b := strings.TrimSpace(string(out))
	if b == "" {
		return "HEAD"
	}
	return b
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func shortID(s string) string {
	if len(s) > 12 {
		return s[:8] + "…" + s[len(s)-3:]
	}
	return s
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(n)/float64(div), "KMGTPE"[exp])
}

func joinErrors(errs []error) string {
	var parts []string
	for _, e := range errs {
		parts = append(parts, e.Error())
	}
	return strings.Join(parts, "; ")
}

func errorStrings(errs []error) []string {
	out := make([]string, 0, len(errs))
	for _, e := range errs {
		out = append(out, e.Error())
	}
	return out
}

func printJSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}

func envDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// parseOnePositional accepts flags before or after the single positional
// argument, which the standard flag package does not do.
func parseOnePositional(fs *flag.FlagSet, args []string) (string, error) {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") && a != "-" {
			flags = append(flags, a)
			if !strings.Contains(a, "=") && !isBoolFlag(fs, a) && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		} else {
			pos = append(pos, a)
		}
	}
	if err := fs.Parse(flags); err != nil {
		return "", err
	}
	if len(pos) != 1 {
		return "", fmt.Errorf("need exactly one positional argument, got %d", len(pos))
	}
	return pos[0], nil
}

func isBoolFlag(fs *flag.FlagSet, name string) bool {
	name = strings.TrimLeft(name, "-")
	f := fs.Lookup(name)
	if f == nil {
		return false
	}
	bf, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && bf.IsBoolFlag()
}
