package render

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func TestPercent(t *testing.T) {
	if got := percent(1, 3); got != 33 {
		t.Fatalf("got %d", got)
	}
	if got := percent(1, 0); got != 0 {
		t.Fatalf("got %d", got)
	}
}

func TestCompactSession(t *testing.T) {
	if got := CompactSession("agentdeck_feature-work_5a5b477b"); got != "feature-work_5a5" {
		t.Fatalf("got %q", got)
	}
	if got := CompactSession("agentdeck_my_project_abcdef12"); got != "my_project_abc" {
		t.Fatalf("got %q", got)
	}
}

func TestScanContextPercentUsesLatestTokenCount(t *testing.T) {
	input := strings.Join([]string{
		`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"total_tokens":50000},"model_context_window":200000}}}`,
		`{"type":"event_msg","payload":{"type":"other"}}`,
		`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"total_tokens":156000},"model_context_window":258400}}}`,
	}, "\n")
	got, ok := scanContextPercent(strings.NewReader(input))
	if !ok || got != 58 {
		t.Fatalf("got %d, ok %v; want 58, true", got, ok)
	}
}

func TestScanContextPercentKeepsOverflow(t *testing.T) {
	input := `{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"total_tokens":232000},"model_context_window":212000}}}`
	got, ok := scanContextPercent(strings.NewReader(input))
	if !ok || got != 110 {
		t.Fatalf("got %d, ok %v; want 110, true", got, ok)
	}
}

func TestScanContextPercentResetsAtCompaction(t *testing.T) {
	input := strings.Join([]string{
		`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"total_tokens":220000},"model_context_window":258400}}}`,
		`{"type":"compacted","payload":{"message":"summary"}}`,
	}, "\n")
	got, ok := scanContextPercent(strings.NewReader(input))
	if !ok || got != 0 {
		t.Fatalf("got %d, ok %v; want 0, true", got, ok)
	}
}

func TestScanContextPercentUsesPostCompactionCount(t *testing.T) {
	input := strings.Join([]string{
		`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"total_tokens":220000},"model_context_window":258400}}}`,
		`{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"ContextCompaction"}}}`,
		`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"total_tokens":30000},"model_context_window":258400}}}`,
	}, "\n")
	got, ok := scanContextPercent(strings.NewReader(input))
	if !ok || got != 7 {
		t.Fatalf("got %d, ok %v; want 7, true", got, ok)
	}
}

func TestParseAgentDeckEnvironmentIdentifiesCodex(t *testing.T) {
	metadata := parseAgentDeckEnvironment("AGENTDECK_INSTANCE_ID=abc-123\nCODEX_SESSION_ID=thread-456\n")
	if metadata.instanceID != "abc-123" || !metadata.codex || metadata.codexSessionID != "thread-456" {
		t.Fatalf("unexpected metadata: %#v", metadata)
	}
	metadata = parseAgentDeckEnvironment("AGENTDECK_INSTANCE_ID=abc-123\nCLAUDE_SESSION_ID=claude-456\n")
	if metadata.instanceID != "abc-123" || metadata.codex || metadata.claudeSessionID != "claude-456" {
		t.Fatalf("unexpected Claude metadata: %#v", metadata)
	}
}

func TestLoadClaudeState(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	path := claudeStatePath("claude-456")
	stateJSON := `{"fetched_at":2000000000,"context_percent":36,"seven_day":{"used_percent":42,"window_duration_mins":10080,"resets_at":2000604800}}`
	if err := os.WriteFile(path, []byte(stateJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	state, ok := loadClaudeState("claude-456")
	if !ok || state.ContextPercent != 36 {
		t.Fatalf("unexpected Claude state: %#v, ok %v", state, ok)
	}
}

func TestLoadClaudeStateRejectsInvalidSessionID(t *testing.T) {
	if state, ok := loadClaudeState("../../other-session"); ok {
		t.Fatalf("unexpected Claude state: %#v", state)
	}
}

func TestTMUXLineSuppressesClaudeSession(t *testing.T) {
	bin := t.TempDir()
	t.Setenv("TMPDIR", t.TempDir())
	writeExecutable(t, filepath.Join(bin, "tmux"), `#!/bin/sh
printf '%s\n' 'AGENTDECK_INSTANCE_ID=instance-1' 'CLAUDE_SESSION_ID=claude-456'
`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	stateJSON := `{"fetched_at":2000000000,"context_percent":36,"seven_day":{"used_percent":42,"window_duration_mins":10080,"resets_at":2000604800}}`
	if err := os.WriteFile(claudeStatePath("claude-456"), []byte(stateJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := LineWithSessionTMUX("", "agentdeck_test_abcdef12"); got != "" {
		t.Fatalf("Claude tmux footer should be suppressed, got %q", got)
	}
}

func TestClaudeLineANSIRendersContextAboveOneHundred(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	stateJSON := `{"fetched_at":2000000000,"context_percent":116,"seven_day":{"used_percent":53,"window_duration_mins":10080,"resets_at":2000604800}}`
	if err := os.WriteFile(claudeStatePath("claude-789"), []byte(stateJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	plain := ansiPattern.ReplaceAllString(LineForClaudeANSI("claude-789", "agentdeck_test_abcdef12"), "")
	for _, want := range []string{"116%"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("line %q does not contain %q", plain, want)
		}
	}
}

func TestClaudeLineANSIRendersSharedFooterContent(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	stateJSON := `{"fetched_at":2000000000,"context_percent":36,"seven_day":{"used_percent":42,"window_duration_mins":10080,"resets_at":2000604800}}`
	if err := os.WriteFile(claudeStatePath("claude-456"), []byte(stateJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	got := LineForClaudeANSI("claude-456", "agentdeck_test_abcdef12")
	plain := ansiPattern.ReplaceAllString(got, "")
	for _, want := range []string{"36%", "test_abc"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("line %q does not contain %q", plain, want)
		}
	}
	if strings.Contains(got, "#[") {
		t.Fatalf("native Claude footer contains tmux styling: %q", got)
	}
}

func TestContextPercentPrefersRolloutOverNativeFooter(t *testing.T) {
	bin := t.TempDir()
	rollout := filepath.Join(t.TempDir(), "rollout.jsonl")
	event := `{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"total_tokens":156000},"model_context_window":258400}}}`
	if err := os.WriteFile(rollout, []byte(event+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(bin, "tmux"), `#!/bin/sh
case "$1" in
  show-environment) printf '%s\n' 'AGENTDECK_INSTANCE_ID=instance-1' 'CODEX_SESSION_ID=thread-1' ;;
  capture-pane) printf '%s\n' 'Context 22% used' ;;
  *) exit 1 ;;
esac
`)
	writeExecutable(t, filepath.Join(bin, "agent-deck"), `#!/bin/sh
printf '%s\n' "$TEST_ROLLOUT"
`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TEST_ROLLOUT", rollout)

	got, ok := ContextPercent("agentdeck_test_abcdef12")
	if !ok || got != 58 {
		t.Fatalf("got %d, ok %v; want rollout value 58, true", got, ok)
	}
}

func TestContextPercentFallsBackToNativeFooter(t *testing.T) {
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "tmux"), `#!/bin/sh
case "$1" in
  show-environment) printf '%s\n' 'AGENTDECK_INSTANCE_ID=instance-1' 'CODEX_SESSION_ID=thread-1' ;;
  capture-pane) printf '%s\n' 'Context 22% used' ;;
  *) exit 1 ;;
esac
`)
	writeExecutable(t, filepath.Join(bin, "agent-deck"), "#!/bin/sh\nexit 1\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	got, ok := ContextPercent("agentdeck_test_abcdef12")
	if !ok || got != 22 {
		t.Fatalf("got %d, ok %v; want native value 22, true", got, ok)
	}
}

func TestContextPercentFallsBackToCodexSessionRollout(t *testing.T) {
	bin := t.TempDir()
	codexHome := t.TempDir()
	rolloutDir := filepath.Join(codexHome, "sessions", "2026", "09", "17")
	if err := os.MkdirAll(rolloutDir, 0o700); err != nil {
		t.Fatal(err)
	}
	rollout := filepath.Join(rolloutDir, "rollout-2026-09-17T16-32-31-thread-1.jsonl")
	event := `{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"total_tokens":156000},"model_context_window":258400}}}`
	if err := os.WriteFile(rollout, []byte(event+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(bin, "tmux"), `#!/bin/sh
case "$1" in
  show-environment) printf '%s\n' 'AGENTDECK_INSTANCE_ID=instance-1' 'CODEX_SESSION_ID=thread-1' ;;
  capture-pane) exit 1 ;;
  *) exit 1 ;;
esac
`)
	writeExecutable(t, filepath.Join(bin, "agent-deck"), "#!/bin/sh\nexit 1\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CODEX_HOME", codexHome)

	got, ok := ContextPercent("agentdeck_test_abcdef12")
	if !ok || got != 58 {
		t.Fatalf("got %d, ok %v; want direct rollout value 58, true", got, ok)
	}
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestParseNativeContextPercentUsesVisibleFooter(t *testing.T) {
	input := "old transcript text: Context 85% used\n\n  wt/gentle-salmon · Context 22% used · weekly 90% left\n"
	got, ok := parseNativeContextPercent(input)
	if !ok || got != 22 {
		t.Fatalf("got %d, ok %v; want 22, true", got, ok)
	}
}

func TestParseNativeContextPercentKeepsCodexOverflow(t *testing.T) {
	got, ok := parseNativeContextPercent("Context 101% used")
	if !ok || got != 101 {
		t.Fatalf("got %d, ok %v; want 101, true", got, ok)
	}
}

func TestLoadClaudeStateKeepsContextOverflow(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	if err := os.WriteFile(claudeStatePath("claude-789"), []byte(`{"fetched_at":2000000000,"context_percent":146}`), 0o600); err != nil {
		t.Fatal(err)
	}
	state, ok := loadClaudeState("claude-789")
	if !ok || state.ContextPercent != 146 {
		t.Fatalf("unexpected Claude state: %#v, ok %v", state, ok)
	}
}

func TestParseNativeContextPercentRejectsMissing(t *testing.T) {
	if got, ok := parseNativeContextPercent("Context unknown"); ok {
		t.Fatalf("got %d, true; want unavailable", got)
	}
}

func TestScanContextPercentRejectsMissingWindow(t *testing.T) {
	input := `{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"total_tokens":50000}}}}`
	if got, ok := scanContextPercent(strings.NewReader(input)); ok {
		t.Fatalf("got %d, true; want unavailable", got)
	}
}
