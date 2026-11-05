# Research: migrating Pi sessions to Claude Code, and OpenCode V2 + Claude subscriptions

Research date: 2026-11-05. Tested against Pi session format v3 and Claude Code 2.1.318 on macOS.

## 1. Summary

- Pi stores sessions as tree-shaped JSONL under `~/.pi/agent/sessions/`.
- Claude Code stores sessions as parent-linked JSONL under `~/.claude/projects/<encoded-cwd>/`.
- The formats map cleanly if you follow Pi's active leaf, compaction, and context edits, and if you emit Claude Code's UUID chain, tool pairing, and optional metadata rows.
- Claude Code's transcript format is internal and undocumented; it must be validated against a pinned Claude Code release.
- OpenCode V2.2.3+ can use a Claude Code subscription through its built-in **Claude Agent** provider, which runs the user's installed `claude` binary via Anthropic's Claude Agent SDK. Its raw Anthropic provider is API-key-only.
- Anthropic's terms allow an end user to sign in to the **unmodified Claude Code binary** with their own subscription, but prohibit third-party developers from offering Claude.ai login or routing plan credentials on users' behalf. OpenCode's design is aimed at the allowed path, but it is not a formal approval; Enterprise contracts and admin policy still govern.

## 2. Pi session format

### Location and naming

```text
~/.pi/agent/sessions/--<munged-cwd>--/<timestamp>_<session-id>.jsonl
```

Pi removes the leading path separator and replaces `/`, `\`, and `:` with `-`, wrapped in `--`.

### Structure

One JSON object per line.

First line is a header:

```json
{"type":"session","version":3,"id":"uuid","timestamp":"2026-01-01T00:00:00.000Z","cwd":"/path","parentSession":"optional"}
```

Every other entry has `type`, `id`, `parentId`, and `timestamp`. The tree is formed by `id`/`parentId`. Pi's current leaf is the **last entry with an id** (SessionManager sets `leafId` to each appended entry while loading).

Entry types observed in the local corpus:

| Type | Meaning | Participates in context? |
|---|---|---|
| `message` | User/assistant/tool result/system message | Yes |
| `custom_message` | Extension-injected message | Yes (as a user message) |
| `branch_summary` | Summary of an abandoned branch | Yes |
| `compaction` | Summary plus `firstKeptEntryId` | Yes (replaces older entries) |
| `context_edit` | Append-only edit/omission of an earlier entry | Changes context, no message |
| `custom` | Extension state | No |
| `usage` | Token accounting | No |
| `model_change` | Model switch | No |
| `thinking_level_change` | Reasoning level switch | No |
| `label` | Bookmark | No |
| `session_info` | Session display name | No |

Message content blocks:

- `text`
- `thinking` (`thinking`, `thinkingSignature`, optional `redacted`)
- `toolCall` (`id`, `name`, `arguments`)
- `image` (`data` base64, `mimeType`)

Message roles: `user`, `assistant`, `toolResult`, `system`, `bashExecution`, `custom`, plus branch/compaction summaries when built.

Assistant messages carry `provider`, `model`, `usage`, `stopReason`, and optional `responseId`.

### Context reconstruction

Pi's rules (from `docs/session-format.md` and `session-manager.js`):

1. Walk `parentId` from the current leaf to the root.
2. Find the latest `compaction` on that path.
3. If present, context is:
   - the compaction entry first,
   - non-system entries from `firstKeptEntryId` up to (not including) the compaction,
   - all entries after the compaction.
4. Apply the latest `context_edit` for each selected entry. `replacement: null` omits the target; non-null replaces content.
5. Convert `message`, `custom_message`, `branch_summary`, and `compaction` to model messages; skip `custom`, `usage`, `model_change`, `thinking_level_change`, `label`, `session_info`.

### Local corpus stats

- 162 sessions, 542.4 MB, max 83.8 MB, median 465 KB.
- 80,484 message entries; 37,230 tool calls; 27,616 thinking blocks; 271 images.
- 86 compactions; 77 context edits; 5,550 custom messages; 190 system messages.
- 23 sessions with a missing/nonexistent cwd.
- 2 sessions with multiple leaves; 3 forks (`parentSession`).
- All version 3, one root each, no duplicate session IDs.
- Most history is non-Anthropic (CommandCode/DeepSeek, OpenAI, OpenCode Go, Cursor), with a large Anthropic minority.

## 3. Claude Code session format

### Location and project encoding

```text
~/.claude/projects/<project>/<session-id>.jsonl
```

`<project>` is the working directory with every non-ASCII-alphanumeric character replaced by `-`:

```js
cwd.replace(/[^a-zA-Z0-9]/g, "-")
```

For directories whose encoded name exceeds 200 characters, Claude Code keeps the first 200 and appends `-` plus a hash:

```js
const h = (s) => { let e = 0; for (let i = 0; i < s.length; i++) e = (e << 5) - e + s.charCodeAt(i) | 0; return e; };
sanitize(s) = s.replace(/[^a-zA-Z0-9]/g, "-");
project(s) = sanitize(s).length <= 200 ? sanitize(s) : sanitize(s).slice(0, 200) + "-" + Math.abs(h(s)).toString(36);
```

Verified against a real long-path project directory: `/private/tmp/cc-probe/long/segment1…segment30` → `…-segment18-se-20u9hl`.

Claude Code resolves symlinks for comparison/storage in practice; `pi2claude` canonicalizes the cwd before encoding.

### Rows

Observed row types in Claude Code 2.1.318:

- `user`, `assistant`, `system`, `attachment`
- Metadata: `mode`, `permission-mode`, `last-prompt`, `ai-title`, `custom-title`, `file-history-snapshot`, `atis-latch`, `api-request`, `api-request-blob`, `api-request-shape`, `cost-state`, `queue-operation`, `pr-link`, `history-suppression`, etc.

Conversational rows share:

```json
{
  "uuid": "...",
  "parentUuid": "... or null",
  "timestamp": "ISO-8601",
  "sessionId": "...",
  "session_id": "...",
  "cwd": "/path",
  "gitBranch": "main",
  "version": "2.1.318",
  "userType": "external",
  "entrypoint": "cli",
  "isSidechain": false
}
```

Assistant rows wrap an Anthropic API message:

```json
{"type":"assistant","message":{
  "id":"msg_...","type":"message","role":"assistant","model":"claude-...",
  "content":[{"type":"text","text":"..."}],
  "stop_reason":"end_turn","stop_sequence":null,
  "usage":{...}
},"requestId":"req_...","apiBlockIndex":0}
```

Claude Code normally writes **one assistant row per content block**, sharing `message.id`, `requestId`, and `usage`. `pi2claude` does the same.

User prompts use `message.content` as a string. Tool results are `type:"user"` rows whose content is an array of `tool_result` blocks referencing `tool_use_id`.

### Compaction

Claude Code represents a compaction as:

- a `system` row with `subtype:"compact_boundary"` and `compactMetadata`;
- a user row with `isCompactSummary: true` and `isVisibleInTranscriptOnly: true` containing the summary.

On resume, Claude opens at the last compaction boundary and loads the summary, the unsummarized messages, and everything after.

### Resume behavior

- `claude --resume <id>` searches the current project, worktrees, then other projects.
- `claude --resume <absolute-path>` loads a transcript at an arbitrary path. This is useful for exports and testing.
- The `/resume` picker filters by recorded cwd, worktrees, `isSidechain`, `sessionKind`, `entrypoint`, `teamName`, and `/loop` sessions.
- A transcript with no parent chain can be loaded, but reconstruction walks `parentUuid` from the last record; unlinked records are dropped.
- Unknown metadata row types are ignored.

### Validation and compatibility

Claude Code's docs explicitly say the entry format is internal and can change between releases. The converter therefore:

- pins the row `version` to the detected `claude --version` (or `--cc-version`);
- validates every generated transcript;
- keeps a provenance marker (`pi-migration`) and a manifest for idempotency.

## 4. Converter mapping

`pi2claude` is the implementation in this repository. Key mappings:

| Pi | Claude Code |
|---|---|
| Active path from last entry | Linear `uuid`/`parentUuid` chain |
| Latest `compaction` | `system/compact_boundary` + `isCompactSummary` user row |
| `context_edit` | Applied to the selected content before conversion |
| User text/image | `user` row; string for text-only, block array for images |
| Assistant text | `assistant` row with a `text` block |
| Anthropic signed thinking | `thinking` or `redacted_thinking` block |
| Unsigned/non-Anthropic thinking | Dropped (optional `--thinking text` converts to text) |
| `toolCall` | `tool_use`; known Pi tools mapped to Claude names and input shapes |
| `toolResult` | `user` row with `tool_result`; images preserved |
| `bashExecution` | `user` row with `<bash-input>` / `<bash-stdout>`, `isMeta` |
| `custom_message` | `user` row, `isMeta` |
| `branch_summary` | `user` row, `isMeta` |
| Pi `system` messages | Skipped: Claude Code builds its own system prompt |
| `session_info.name` | `custom-title` row |
| Last user prompt | `last-prompt` row with `leafUuid` |

Tool calls are paired before emission. A `tool_use` without a result on the active branch is dropped; a `tool_result` without a surviving call is dropped. This guarantees the Anthropic Messages API contract.

### Determinism

- Session ID: reuse the Pi UUID when canonical; otherwise UUIDv5.
- Entry UUIDs: UUIDv5 of `claudeSessionID:row:<index>`; stable across runs.
- Message/request IDs: SHA-1-derived.
- Timestamps: Pi entry timestamp, then message timestamp, then header timestamp.
- No conversion timestamp in the transcript.
- Two conversions of the same session to different Claude config dirs produced byte-identical files.

### Models

Anthropic model IDs are preserved. Non-Anthropic history is stamped with a fallback (`--model`, default `claude-sonnet-5-5`). Claude Code resumes on the last assistant model, so the fallback determines the resumed model for sessions that ended on a non-Anthropic provider.

## 5. Empirical validation

Static:

- All 162 local Pi sessions converted in dry-run with zero validation failures.
- Output: ~167 MB, 47,224 rows, 3,447 signed thinking blocks kept, 7,338 unsigned/foreign thinking blocks dropped, 17 unmatched tool calls dropped, 143 images.
- `pi2claude validate` checks the UUID chain, session ID, sidechain flag, message shape, thinking signatures, and tool pairing.

Live:

- Converted a 4.8 KB Pi session (`01a09c43-305b-7290-865a-5e562c761afa`) with 3 Anthropic assistant turns, 2 signed thinking blocks, and 2 bash tool calls.
- Ran `claude --resume /tmp/live-test.jsonl -p "Reply with exactly: OK"`.
- Claude loaded the transcript, replayed the signed thinking and tool history without an API error, and returned `OK`.
- The run cost $0.214697 (10,130 cache-creation tokens, 11,877 cache-read tokens) and did not install anything into `~/.claude`.

This is evidence, not a guarantee: a future Claude Code release can change signature replay or transcript validation.

## 6. OpenCode V2 and Claude Code subscriptions

### Technical answer

OpenCode V2 has two relevant paths:

1. **Anthropic provider** (`anthropic`):
   - Configured with an API key or auth token (`ANTHROPIC_API_KEY`, `--method key`).
   - No Claude.ai OAuth login. The old Claude Max/Pro plugin was removed in OpenCode 1.3.0 after Anthropic legal requests.
2. **Claude Agent provider** (`claude-agent`, built into core):
   - Added in OpenCode **v2.2.3** (2026-10-29): “Use Claude Code in OpenCode: Install Claude Code, run `claude auth login`, then select **Claude Agent** in `/connect` to use your Claude Code login and models.”
   - Current release at research time: **v2.2.5** (2026-10-30).
   - It runs the user's installed, unmodified `claude` binary through Anthropic's Claude Agent SDK.
   - It never reads Claude Code's saved login or sees the token. The user signs in with `claude auth login`; `/connect` only records the choice.
   - The provider spec says: “Any existing login works: a subscription, an API key or a cloud account.”
   - It requires `claude` installed and signed in on the same machine; it is skipped in hosted/remote workspaces.

So: **OpenCode V2 does not let you paste a Claude subscription OAuth token into its Anthropic provider, but its Claude Agent provider does let you use a Claude Code subscription login by delegating to the installed Claude Code binary.** That includes a Team/Enterprise seat, provided the local `claude` login is entitled to Claude Code.

### Legal answer

Anthropic's legal/compliance page says:

- OAuth authentication is intended exclusively for purchasers of Claude Free, Pro, Max, Ultra, Team, and Enterprise plans and is designed to support ordinary use of Claude Code and other native Anthropic applications.
- Developers building products with the Agent SDK should use API-key authentication.
- “Anthropic does not permit third-party developers to offer Claude.ai login into their own applications, or to route requests through Free, Pro, Max, or Ultra plan credentials on behalf of their users.”
- “Nor does it prevent an end user from signing in to the unmodified Claude Code binary with their own Claude subscription, including where a platform hosts Claude Code as described above.”

OpenCode's Claude Agent design is deliberately built around the last sentence:

- It runs the user's own `claude`, unmodified.
- It does not intermediate credentials or make requests itself.
- It does not resell or pool usage.
- The user authenticates directly with Anthropic.

That is the strongest available argument that the setup is permitted. It is not a formal approval:

- The OpenCode spec itself lists open questions, including whether it should discuss the integration with Anthropic.
- Anthropic has blocked third-party OAuth use before (OpenCode/OpenClaw, January/April 2026) and reserves the right to enforce.
- Team/Enterprise use is governed by the organization's Commercial Terms and admin settings. An Enterprise admin may prohibit third-party harnesses even if the seat includes Claude Code.
- Anthropic's “Can customers offer Claude Code in their products?” section requires the binary to be unmodified and each end user to authenticate with their own credentials; OpenCode is local software, not a hosted resale service, which helps.
- This is not legal advice. For a company deployment, get approval from the Enterprise admin/Anthropic account team.

### Practical recommendation

- For individual use: OpenCode V2.2.3+ → `/connect` → **Claude Agent** is the supported-by-design route. Keep Claude Code installed and updated, and sign in with `claude auth login`.
- For a product or hosted service: use API keys or a supported cloud provider (Bedrock/Vertex/Foundry). Do not build a Claude.ai OAuth login into your own app or resell seats.
- For Enterprise: confirm with the org owner that third-party clients are allowed and that the seat includes Claude Code.

## 7. Sources

Pi:

- Local Pi docs: `~/.nvm/versions/node/v24.12.0/lib/node_modules/@earendil-works/pi-coding-agent/docs/session-format.md`
- Local Pi docs: `.../docs/message-types.md`
- `.../dist/core/session-manager.js`

Claude Code:

- Sessions: https://code.claude.com/docs/en/sessions
- Legal and compliance: https://code.claude.com/docs/en/legal-and-compliance
- Agent SDK session storage: https://code.claude.com/docs/en/agent-sdk/session-storage
- Agent SDK overview: https://code.claude.com/docs/en/agent-sdk/overview
- Observed transcripts in `~/.claude/projects/` and Claude Code 2.1.318 binary strings.

OpenCode:

- Release v2.2.3: https://github.com/anomalyco/opencode/releases/tag/v2.2.3
- Claude Agent provider spec: https://github.com/anomalyco/opencode/blob/dev/specs/v2/claude-agent-provider.md
- Provider integration: https://github.com/anomalyco/opencode/blob/dev/packages/core/src/plugin/provider/claude-agent/integration.ts
- Anthropic provider: https://github.com/anomalyco/opencode/blob/dev/packages/ai/src/providers/anthropic.ts
- Providers docs: https://opencode.ai/v2/docs/providers
- Auth docs: https://opencode.ai/v2/docs/cli/providers

Anthropic subscription/enterprise:

- Legal and compliance: https://code.claude.com/docs/en/legal-and-compliance
- Pro/Max/Ultra: https://support.claude.com/en/articles/11145838-using-claude-code-with-your-pro-or-max-plan
- Team/Enterprise: https://support.claude.com/en/articles/11845131
