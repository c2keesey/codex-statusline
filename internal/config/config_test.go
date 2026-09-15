package config

import (
	"strings"
	"testing"
)

func TestApplyStatusLineAddsTuiSection(t *testing.T) {
	got, changed := ApplyStatusLine("model = \"gpt-5\"\n", []string{"model", "current-dir"})
	if !changed || !strings.Contains(got, "[tui]\n  status_line = [\"model\", \"current-dir\"]") {
		t.Fatalf("unexpected result:\n%s", got)
	}
}

func TestApplyStatusLineReplacesOnlyKey(t *testing.T) {
	in := "model = \"gpt-5\"\n\n[tui]\n  theme = \"sea\"\n  status_line = [\n    \"model\",\n    \"current-dir\",\n  ]\n\n[features]\nfoo = true\n"
	got, changed := ApplyStatusLine(in, []string{"git-branch"})
	if !changed {
		t.Fatal("expected change")
	}
	want := "[tui]\n  theme = \"sea\"\n  status_line = [\"git-branch\"]\n\n[features]"
	if !strings.Contains(got, want) {
		t.Fatalf("unexpected result:\n%s", got)
	}
}

func TestApplyStatusLineIsIdempotent(t *testing.T) {
	in := "[tui]\nstatus_line = [\"model\"]\n"
	got, changed := ApplyStatusLine(in, []string{"model"})
	if changed || got != in {
		t.Fatalf("wanted unchanged, got %q", got)
	}
}

func TestApplyTmuxIsIdempotentAndPreservesExistingStatus(t *testing.T) {
	in := "set -g status-right \"%H:%M\"\n"
	got, changed := ApplyTmux(in)
	if !changed || !strings.Contains(got, in) || !strings.Contains(got, TmuxBlock) {
		t.Fatalf("unexpected result:\n%s", got)
	}
	again, changed := ApplyTmux(got)
	if changed || again != got {
		t.Fatal("second application should be a no-op")
	}
}

func TestApplyAgentDeckAddsOverrideAndIsIdempotent(t *testing.T) {
	in := "[tmux]\n  launch_in_user_scope = false\n  [tmux.options]\n    set-clipboard = \"on\"\n\n[display]\n  theme = \"light\"\n"
	got, changed := ApplyAgentDeck(in)
	if !changed || !strings.Contains(got, "status-left = '") || !strings.Contains(got, "status-left-length = \"200\"") || !strings.Contains(got, "[display]") {
		t.Fatalf("unexpected result:\n%s", got)
	}
	if !strings.Contains(got, `status-left = '  #(~/.local/bin/codex-statusline render --tmux-style`) {
		t.Fatalf("status line does not match Codex's two-column inset:\n%s", got)
	}
	for _, option := range []string{`status-style = "bg=default,fg=default"`, `status-right = ""`, `status-right-length = "0"`, `window-status-format = ""`, `window-status-current-format = ""`} {
		if !strings.Contains(got, option) {
			t.Fatalf("missing %s in:\n%s", option, got)
		}
	}
	again, changed := ApplyAgentDeck(got)
	if changed || again != got {
		t.Fatal("second application should be a no-op")
	}
}
