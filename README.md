# codex-statusline

A Claude Code-style status line for Codex, using Codex's native TUI footer for
live agent data and an optional tmux companion for the signals Codex doesn't
expose. Machine load and weekly usage live in MAIA Deck's header (its
`machines` and `weekly_usage` fields), not here.

The result follows the same information architecture as the original two-line
Claude status line:

| Claude status line | Codex adapter |
| --- | --- |
| branch + dirty count | `git-branch` + `branch-changes` |
| cwd | `current-dir` |
| session summary | `thread-title` |
| context percentage | rollout token events in the Agent Deck companion; `context-used` natively |
| weekly usage | `weekly-limit` |
| model + effort glyph | `model-with-reasoning` |

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
a compact shared row:

```text
  61% │ feature-work_5a5
```

Codex renders the row in tmux. Claude Code renders the same content as the
second row of its native status command and disables the redundant tmux footer
for that session.

The identity retains the Agent Deck display name plus the first three characters
of its tmux suffix. Codex context comes from the latest rollout token-count
event, with the visible native statusline as a fallback. It resets at Codex's
compaction boundary and follows Codex's 12k baseline-token calculation.
Claude Code's configured status command publishes the exact context value from
its JSON payload into a per-session local cache for its native second row.
Context is a plain percentage. The Agent Deck line uses the same color roles as
`claude-statusline` and shares Codex's two-column left inset. It emits no tmux
content unless the session is a recognized Codex conversation.

Both installers are idempotent. Before the first edit they preserve a sibling
`*.codex-statusline.bak` copy of the existing config.

## Privacy

The status line has no telemetry and makes no network requests. It reads local
tmux state, Codex rollout files and the per-session Claude cache. Transcript
content, prompts, credentials, and account identifiers are never copied or
stored.

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

A session whose tmux environment has a light `COLORFGBG` (background index 8
or above, e.g. `0;15`) renders the Sea Glass footer: ocean-colored accents with
bold usage percentages and no inherited dim styling. Agent Deck keeps that
variable on the viewer's theme (`agent-deck theme apply`), so the footer flips
with it on the next status refresh. `CODEX_STATUSLINE_THEME=seaglass` in the
session environment forces the Sea Glass footer regardless. Dark sessions
retain the terminal palette.
