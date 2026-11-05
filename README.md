# pi2claude

Deterministically migrate [Pi coding agent](https://github.com/earendil-works/pi) sessions into Claude Code, so you can open them with `claude --resume` and keep working.

`pi2claude` reads Pi's JSONL session format, follows Pi's active leaf, applies its latest compaction and in-place context edits, and writes a context-faithful Claude Code transcript into the correct `~/.claude/projects/<project>/` directory.

## Build

```bash
go build -o bin/pi2claude .
# or
go install .
```

No runtime dependencies.

## Quick start

```bash
# See what Pi has and where it would go
pi2claude list

# Convert everything, but write nothing
pi2claude migrate --all --dry-run

# Convert everything into ~/.claude
pi2claude migrate --all

# Convert one session
pi2claude migrate --session 01a09c43-305b-7290-865a-5e562c761afa

# Convert one project
pi2claude migrate --project rocinante

# Inspect the mapping without writing
pi2claude inspect --dump 01a09c43-305b-7290-865a-5e562c761afa

# Validate a generated transcript
pi2claude validate ~/.claude/projects/<project>/<session-id>.jsonl
```

After migration:

```bash
cd /path/to/original/project
claude
# then /resume, or:
claude --resume <session-id>
```

You can also resume an exported transcript by path:

```bash
claude --resume /path/to/<session-id>.jsonl
```

## What gets migrated

- The **active branch** Pi is currently on (the last entry in the file is Pi's leaf).
- The latest **compaction** on that branch. Earlier summarized messages are not re-emitted.
- The latest **context edit** per target, including omissions (`replacement: null`).
- User and assistant text.
- Tool calls and tool results as real `tool_use`/`tool_result` blocks.
- Images in user and tool-result content.
- Pi `custom_message` context as hidden user messages.
- Branch summaries as hidden user messages.
- Anthropic-signed thinking blocks. Unsigned or foreign-provider thinking is dropped.
- Session names as Claude Code custom titles.

Pi's system prompt/tool state is not migrated; Claude Code builds its own.

## Fidelity choices

| Pi | Claude Code |
|---|---|
| Active leaf + compaction + context edits | Context-faithful transcript |
| `message` user/assistant | `user`/`assistant` rows |
| `toolCall` | `tool_use` (known Pi tools mapped to Claude names) |
| `toolResult` | `user` row with `tool_result` |
| Anthropic `thinking` with signature | `thinking` / `redacted_thinking` |
| Non-Anthropic or unsigned thinking | Dropped |
| `custom_message` | Hidden user message (`isMeta`) |
| `branch_summary` | Hidden user message |
| `compaction` | `system/compact_boundary` + `isCompactSummary` user row |
| `bashExecution` | `<bash-input>` / `<bash-stdout>` user row |
| `session_info.name` | `custom-title` row |
| Pi system messages | Skipped |

Tool calls are only emitted when their result is also in the active context. Orphan results and calls are dropped and reported as warnings. This keeps the transcript valid for Anthropic's Messages API.

### Tool name mapping

`bash→Bash`, `read→Read`, `write→Write`, `edit→Edit` (single edit), `grep→Grep`, `find→Glob`, single-query `web_search→WebSearch`, single-URL `fetch_content→WebFetch`, `ask`/`questionnaire→AskUserQuestion`.

Everything else, including MCP tools and `codemode`, keeps its Pi name. Historical tool calls are never re-executed.

## Determinism

The same Pi session always produces byte-identical output:

- Session IDs are reused when they are canonical UUIDs; otherwise a UUIDv5 is derived from the Pi session ID.
- Entry UUIDs are UUIDv5 values derived from the Claude session ID, the Pi entry ID, and the row position.
- API message/request IDs are SHA-1-derived.
- Timestamps come from the Pi entry (or the nested message timestamp).
- No conversion timestamp is written into the transcript.
- Rows are emitted in a stable order.

The only non-deterministic file is the manifest, which records conversion time and source hashes.

## Project directory encoding

Claude Code stores transcripts under:

```text
~/.claude/projects/<cwd with non-alphanumerics replaced by ->/<session-id>.jsonl
```

`pi2claude` reproduces Claude Code's exact encoding, including the long-path rule:

- Sanitize every non-`[A-Za-z0-9]` character to `-`.
- If the result is longer than 200 characters, keep the first 200, then append `-` and `Math.abs(javaHash(fullPath)).toString(36)`.

The Java hash is the 32-bit `(h << 5) - h + UTF-16 code unit` loop. This is tested against a real Claude Code directory name.

## Idempotency and safety

`migrate` maintains `~/.claude/pi2claude/manifest.json` with the source hash, target path, and Claude session ID.

- A rerun with an unchanged source reports `up-to-date`.
- A changed source reconverts its own target.
- A target that exists but was not created by `pi2claude` is a `conflict` and is not overwritten unless you pass `--force`.
- Overwrites create a `.bak.<timestamp>` file by default (`--no-backup` disables this).
- Writes are atomic and files are mode `0600`.
- Every transcript starts with a `pi-migration` marker so `pi2claude` can recognize its own output even if the manifest is lost.

## Flags

```text
--pi-dir <dir>        Pi sessions directory (default ~/.pi/agent/sessions)
--claude-dir <dir>    Claude config directory (default $CLAUDE_CONFIG_DIR or ~/.claude)
--all                 migrate every Pi session (required unless filtering)
--session <id|path>   one session (repeatable)
--project <text>      cwd/project-folder substring
--dry-run             write nothing
--force               overwrite existing targets
--no-backup           skip .bak files on overwrite
--model <id>          Claude model for non-Anthropic history (default claude-sonnet-5-5)
--thinking <mode>     signed (default), drop, or text
--cc-version <ver>    version stamped on rows (default: detect `claude --version`)
--cwd <dir>           override every session's cwd
--missing-cwd <mode>  keep (default), skip, current, or remap:<dir>
--no-tool-map         keep Pi tool names
--json                machine-readable output
--verbose             show warnings
--quiet               summary only
```

`list`, `inspect`, and `validate` accept `--json`.

## Missing cwd

23 of the inspected Pi sessions point at a directory that no longer exists. By default `keep` preserves the recorded cwd and still creates the project folder, so `claude --resume <session-id>` can find it. Other options:

```bash
pi2claude migrate --all --missing-cwd skip
pi2claude migrate --all --missing-cwd remap:/path/to/current/checkout
pi2claude migrate --all --cwd /path/to/current/checkout
```

## Validation

`pi2claude` validates every converted transcript before writing:

- JSON parses.
- UUIDs are unique and `parentUuid` points to an earlier row.
- `sessionId` is consistent.
- `isSidechain` is false.
- Assistant messages have ids, usage, and non-empty content.
- Thinking blocks have signatures.
- Every `tool_use` has a matching `tool_result`.
- Every `tool_result` refers to an earlier `tool_use`.
- User/assistant content is non-empty.

Use `pi2claude validate <file>` for any transcript, including ones Claude Code wrote.

## Known limits

- Claude Code's transcript format is internal and can change between releases. This version was tested against Claude Code **2.1.318**. Re-run `pi2claude validate` after upgrading.
- Non-Anthropic assistant history is stamped with the fallback Claude model (`--model`). Claude Code uses the last assistant model when resuming. Run `/model` after resuming if you want a different one.
- Anthropic thinking signatures are preserved. A live resume test with signed thinking and tool history succeeded, but signatures can become invalid if a future Claude Code release changes its replay rules. Use `--thinking drop` if you hit a signature error.
- Pi's system prompt, tool loadout, model switches, labels, and extension state are not migrated.
- Abandoned Pi branches other than the active leaf are not migrated. Branch summaries on the active path are.
- Huge sessions are migrated in full after Pi's compaction. The inspected corpus produced ~167 MB of Claude transcripts from ~542 MB of Pi sessions. A single very large session can still be slow to resume.
- Transcripts contain source code, command output, and potentially secrets. They are written `0600`; protect `~/.claude` accordingly.

## Development

```bash
go test ./...
go vet ./...
go build ./...
```

Tests cover project-directory encoding (including the long-path hash), UUIDv5 determinism, Pi leaf/compaction/context-edit reconstruction, tool pairing, thinking filtering, model mapping, validator invariants, and byte-for-byte deterministic output.

See [RESEARCH.md](RESEARCH.md) for the format research and the OpenCode V2 / Claude subscription findings.
