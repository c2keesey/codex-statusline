# codex-statusline

A Claude Code-style status line for Codex, using Codex's native TUI footer for
live agent data and an optional tmux companion for the machine signals Codex
doesn't expose.

The result follows the same information architecture as the original two-line
Claude status line:

| Claude status line | Codex adapter |
| --- | --- |
| branch + dirty count | `git-branch` + `branch-changes` |
| cwd | `current-dir` |
| session summary | `thread-title` |
| context percentage | `context-used` plus the Agent Deck tmux companion |
| weekly usage | `weekly-limit` |
| model + effort glyph | `model-with-reasoning` |
| weekly usage + burn pace | local Codex app-server snapshot, cached for 30 seconds |
| CPU + memory | optional tmux companion (`↯` + `⛁`) |

The native pieces update inside the Codex pane and survive tmux, Agent Deck,
SSH, and terminal resizes. Codex does not currently accept an arbitrary command
renderer for its footer, so the tmux layer is deliberately small.

## Install

Requires Go 1.24+ and Codex CLI with `tui.status_line` support.

```bash
go install github.com/c2keesey/codex-statusline/cmd/codex-statusline@latest
codex-statusline install
```

Make sure `$(go env GOPATH)/bin` is on your `PATH`, then restart Codex. To add
CPU and memory state to ordinary tmux:

```bash
codex-statusline install --tmux
```

Agent Deck uses its own isolated tmux server and per-session status bar. Use:

```bash
codex-statusline install --agent-deck
```

This replaces Agent Deck's redundant tmux session/window text and key hints with
a compact Claude-style row:

```text
  61% │ 7d ▰▱▱▱ 30% ⇡0.8× │ ↯42 ⛁32 │ feature-work_5a5
```

The identity retains the Agent Deck display name plus the first three characters
of its tmux suffix. Context comes from Codex's live native statusline, with the
latest rollout token-count event as a fallback. The fallback resets at Codex's
compaction boundary and follows Codex's 12k baseline-token calculation. Usage
comes from Codex's local `account/rateLimits/read`
snapshot; a missing window renders as `—` rather than being reported as zero.
Weekly usage uses the same four-cell, 25%-per-cell gauge as the Claude line;
context stays a plain percentage. The Agent Deck line uses the same color roles
as `claude-statusline` and shares Codex's two-column left inset. It emits no
content unless the session has a Codex conversation, so Claude Code sessions
keep only their native statusline.
The `⇡` value is actual weekly spend divided by expected spend at the current
point in the rolling window, so `1.0×` is on pace and `1.4×` is 40% hot. Its
pace schedule matches `claude-statusline`: weekdays carry weight `1.0`, weekend
days carry `0.5`, and reset-to-reset day buckets use the weight of the local
calendar day on which they begin. Codex also reads Claude's shared
`~/.claude/statusline-schedule` file for weekday and dated overrides. Set
`CODEX_STATUSLINE_SCHEDULE` to use a different file just for Codex.

Both installers are idempotent. Before the first edit they preserve a sibling
`*.codex-statusline.bak` copy of the existing config.

## Privacy

The status line has no telemetry and makes no network requests of its own. It
reads local tmux state, machine load, and Codex's local app-server response.
The cache contains only rate-limit percentages and reset timestamps, is stored
in the system temp directory with user-only permissions, and expires after 30
seconds. Transcript content, prompts, credentials, and account identifiers are
never copied or stored.

## Commands

```text
codex-statusline install [--tmux] [--agent-deck]
codex-statusline render [--cwd PATH]
codex-statusline doctor
codex-statusline preset
```

`preset` prints the exact `tui.status_line` value for manual use. `render` is
the fast, dependency-free command called by tmux every two seconds.

## Why native-first?

Codex provides an interactive `/statusline` picker and persists the selection
as `tui.status_line` in `~/.codex/config.toml`. The native fields include model,
reasoning, context, limits, git, token counters, session identity, and working
directory. See the official [Codex developer commands](https://developers.openai.com/codex/cli/slash-commands/)
and [configuration reference](https://developers.openai.com/codex/config-reference/).

## Development

```bash
go test ./...
go vet ./...
```

MIT licensed.

### Sea Glass footer

For a light Sea Glass tmux session, set `status-style` to
`fg=#15333A,bg=#E9F2EF` and set the tmux session environment variable
`CODEX_STATUSLINE_THEME=seaglass`. The styled renderer uses ocean-colored
accents with bold usage percentages and clears inherited dim styling.
Other sessions retain the terminal palette.
