package render

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
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
	if !ok || state.ContextPercent != 36 || state.SevenDay == nil || state.SevenDay.UsedPercent != 42 {
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
	for _, want := range []string{"116%", "7d", "53%"} {
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
	for _, want := range []string{"36%", "7d", "42%", "↯", "⛁", "test_abc"} {
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

func TestParseAgentDeckUsageMapsSevenDayWindow(t *testing.T) {
	got, err := parseAgentDeckUsage([]byte(`{"providers":[
		{"id":"claude","label":"Claude","windows":[{"kind":"seven_day","label":"7d","used_percentage":74,"resets_at":1790672400}],"updated_at":1790652860,"stale":false},
		{"id":"codex","label":"Codex","plan":"pro","windows":[
			{"kind":"five_hour","label":"5h","used_percentage":12.0,"resets_at":1790000000},
			{"kind":"seven_day","label":"7d","used_percentage":40.0,"resets_at":1790500000}
		],"updated_at":1790000000,"stale":false}]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := UsageWindow{UsedPercent: 40, WindowDurationMins: 10080, ResetsAt: 1790500000}
	if got.SevenDay == nil || *got.SevenDay != want {
		t.Fatalf("seven-day = %#v, want %#v", got.SevenDay, want)
	}
}

func TestParseAgentDeckUsageRoundsFractionalPercent(t *testing.T) {
	got, err := parseAgentDeckUsage([]byte(`{"providers":[{"id":"codex","windows":[{"kind":"seven_day","used_percentage":39.6}]}]}`))
	if err != nil || got.SevenDay == nil || got.SevenDay.UsedPercent != 40 {
		t.Fatalf("usage = %#v, err = %v", got.SevenDay, err)
	}
}

func TestParseAgentDeckUsageMissingResetHasNoPace(t *testing.T) {
	got, err := parseAgentDeckUsage([]byte(`{"providers":[{"id":"codex","windows":[{"kind":"seven_day","label":"7d","used_percentage":40}]}]}`))
	if err != nil || got.SevenDay == nil {
		t.Fatalf("usage = %#v, err = %v", got.SevenDay, err)
	}
	if got.SevenDay.ResetsAt != 0 {
		t.Fatalf("resets_at = %d, want 0", got.SevenDay.ResetsAt)
	}
	if line := weeklyUsage(got.SevenDay, time.Now()); line != "7d ▰▰▱▱ 40%" {
		t.Fatalf("line = %q", line)
	}
}

func TestParseAgentDeckUsageKeepsLastKnownNumbersWithError(t *testing.T) {
	got, err := parseAgentDeckUsage([]byte(`{"providers":[{"id":"codex","windows":[{"kind":"seven_day","used_percentage":55,"resets_at":1790500000}],"error":"codex: app-server timed out","stale":false}]}`))
	if err != nil || got.SevenDay == nil || got.SevenDay.UsedPercent != 55 {
		t.Fatalf("usage = %#v, err = %v", got.SevenDay, err)
	}
}

func TestParseAgentDeckUsageErrorWithoutNumbersFails(t *testing.T) {
	if _, err := parseAgentDeckUsage([]byte(`{"providers":[{"id":"codex","windows":[],"error":"codex: not signed in"}]}`)); err == nil {
		t.Fatal("expected an error so the renderer keeps its last-known value")
	}
}

func TestParseAgentDeckUsageRendersStaleSnapshot(t *testing.T) {
	got, err := parseAgentDeckUsage([]byte(`{"providers":[{"id":"codex","windows":[{"kind":"seven_day","used_percentage":61,"resets_at":1790500000}],"stale":true}]}`))
	if err != nil || got.SevenDay == nil || got.SevenDay.UsedPercent != 61 {
		t.Fatalf("usage = %#v, err = %v", got.SevenDay, err)
	}
}

func TestParseAgentDeckUsageWithoutSevenDayWindowHidesGauge(t *testing.T) {
	got, err := parseAgentDeckUsage([]byte(`{"providers":[{"id":"codex","windows":[{"kind":"five_hour","used_percentage":12},{"kind":"other","label":"30d","used_percentage":5}]}]}`))
	if err != nil || got.SevenDay != nil {
		t.Fatalf("usage = %#v, err = %v", got.SevenDay, err)
	}
}

func TestParseAgentDeckUsageRejectsMissingProvider(t *testing.T) {
	if _, err := parseAgentDeckUsage([]byte(`{"providers":[{"id":"claude","windows":[{"kind":"seven_day","used_percentage":74}]}]}`)); err == nil {
		t.Fatal("expected an error when agent-deck has no codex provider")
	}
}

func TestParseAgentDeckUsageRejectsMalformedJSON(t *testing.T) {
	for _, input := range []string{``, `not json`, `{"providers":[`, `{"providers":{"id":"codex"}}`} {
		if _, err := parseAgentDeckUsage([]byte(input)); err == nil {
			t.Fatalf("expected an error for %q", input)
		}
	}
}

// fakeAgentDeck puts an agent-deck stub first on PATH (or no agent-deck at all
// when output is empty) and points the usage cache at a fresh temp dir.
func fakeAgentDeck(t *testing.T, output string) {
	t.Helper()
	bin := t.TempDir()
	if output != "" {
		script := "#!/bin/sh\nprintf '%s\\n' '" + output + "'\n"
		if err := os.WriteFile(filepath.Join(bin, "agent-deck"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	t.Setenv("TMPDIR", t.TempDir())
}

func TestUsageRatesReadsAgentDeck(t *testing.T) {
	fakeAgentDeck(t, `{"providers":[{"id":"codex","windows":[{"kind":"seven_day","used_percentage":40,"resets_at":1790500000}],"updated_at":1790000000,"stale":false}]}`)
	got := UsageRates()
	if got.SevenDay == nil || got.SevenDay.UsedPercent != 40 || got.SevenDay.ResetsAt != 1790500000 {
		t.Fatalf("usage = %#v", got.SevenDay)
	}
}

func TestUsageRatesHidesGaugeWithoutAgentDeck(t *testing.T) {
	fakeAgentDeck(t, "")
	if line := weeklyUsage(UsageRates().SevenDay, time.Now()); line != "7d ▱▱▱▱ —" {
		t.Fatalf("line = %q", line)
	}
}

func TestUsageRatesHidesGaugeWhenAgentDeckLacksCodex(t *testing.T) {
	fakeAgentDeck(t, `{"providers":[{"id":"claude","windows":[{"kind":"seven_day","used_percentage":74}],"stale":false}]}`)
	if line := weeklyUsage(UsageRates().SevenDay, time.Now()); line != "7d ▱▱▱▱ —" {
		t.Fatalf("line = %q", line)
	}
}

func TestBurnRatio(t *testing.T) {
	t.Setenv("CODEX_STATUSLINE_SCHEDULE", filepath.Join(t.TempDir(), "missing"))
	location, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	end := time.Date(2026, time.September, 21, 0, 0, 0, 0, location)
	window := &UsageWindow{UsedPercent: 30, WindowDurationMins: 10080, ResetsAt: end.Unix()}
	now := end.Add(-7 * 24 * time.Hour / 2)
	ratio, ok := burnRatio(window, now)
	if !ok || ratio < 0.513 || ratio > 0.515 {
		t.Fatalf("got ratio %v, ok %v", ratio, ok)
	}
	if got := weeklyUsage(window, now); got != "7d ▰▱▱▱ 30% ⇡0.5×" {
		t.Fatalf("got %q", got)
	}
}

func TestBurnRatioWeightsWeekendsLikeClaudeStatusline(t *testing.T) {
	location, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	end := time.Date(2026, time.September, 21, 0, 0, 0, 0, location)
	window := &UsageWindow{UsedPercent: 70, WindowDurationMins: 10080, ResetsAt: end.Unix()}
	now := time.Date(2026, time.September, 19, 12, 0, 0, 0, location)

	ratio, ok := burnRatioWithSchedule(window, now, paceSchedule{})
	if !ok || ratio < 0.799 || ratio > 0.801 {
		t.Fatalf("got ratio %v, ok %v; want 0.8, true", ratio, ok)
	}
}

func TestBurnRatioHonorsDatedScheduleOverride(t *testing.T) {
	location, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	end := time.Date(2026, time.September, 21, 0, 0, 0, 0, location)
	window := &UsageWindow{UsedPercent: 70, WindowDurationMins: 10080, ResetsAt: end.Unix()}
	now := time.Date(2026, time.September, 19, 12, 0, 0, 0, location)
	schedule := paceSchedule{dates: map[string]float64{"2026-09-19": 0}}

	ratio, ok := burnRatioWithSchedule(window, now, schedule)
	if !ok || ratio < 0.769 || ratio > 0.771 {
		t.Fatalf("got ratio %v, ok %v; want 0.77, true", ratio, ok)
	}
}

func TestUsageGauge(t *testing.T) {
	tests := map[int]string{
		0:   "▱▱▱▱",
		11:  "▱▱▱▱",
		13:  "▰▱▱▱",
		49:  "▰▰▱▱",
		63:  "▰▰▰▱",
		100: "▰▰▰▰",
	}
	for percent, want := range tests {
		if got := usageGauge(percent); got != want {
			t.Errorf("usageGauge(%d) = %q, want %q", percent, got, want)
		}
	}
}

func TestTMUXPercentColorMatchesClaudeRamp(t *testing.T) {
	tests := map[int]string{
		0:   "#[fg=colour10,nodim,bold]",
		60:  "#[fg=colour136,nodim,bold]",
		80:  "#[fg=colour166,nodim,bold]",
		100: "#[fg=colour9,nodim,bold]",
	}
	for percent, want := range tests {
		if got := tmuxPercentColor(percent); got != want {
			t.Errorf("tmuxPercentColor(%d) = %q, want %q", percent, got, want)
		}
	}
}

func TestBurnRatioStabilizationCutoff(t *testing.T) {
	t.Setenv("CODEX_STATUSLINE_SCHEDULE", filepath.Join(t.TempDir(), "missing"))
	location, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	end := time.Date(2026, time.September, 21, 0, 0, 0, 0, location)
	window := &UsageWindow{UsedPercent: 1, WindowDurationMins: 10080, ResetsAt: end.Unix()}
	start := end.Add(-7 * 24 * time.Hour)
	if _, ok := burnRatio(window, start.Add(172*time.Minute)); ok {
		t.Fatal("expected ratio to be hidden before 2% of weighted time elapsed")
	}
	if _, ok := burnRatio(window, start.Add(173*time.Minute)); !ok {
		t.Fatal("expected ratio after 2% of weighted time elapsed")
	}
}

func TestParsePaceScheduleMatchesClaudeFormat(t *testing.T) {
	schedule := parsePaceSchedule(strings.NewReader("fri 0.25\n2026-09-19 # bare date means zero\ninvalid nope\n"))
	if got := schedule.weekdays["fri"]; got != 0.25 {
		t.Fatalf("Friday weight = %v, want 0.25", got)
	}
	if got := schedule.dates["2026-09-19"]; got != 0 {
		t.Fatalf("dated weight = %v, want 0", got)
	}
}

func TestAcquireUsageLockRecoversOrphan(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "usage.lock")
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	stale := now.Add(-usageLockMaxAge - time.Second)
	if err := os.Chtimes(lockPath, stale, stale); err != nil {
		t.Fatal(err)
	}
	lock, ok := acquireUsageLock(lockPath, now)
	if !ok {
		t.Fatal("expected stale lock to be replaced")
	}
	lock.Close()
}

func TestAcquireUsageLockPreservesLiveLock(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "usage.lock")
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if lock, ok := acquireUsageLock(lockPath, time.Now()); ok {
		lock.Close()
		t.Fatal("expected fresh lock to remain owned by the other renderer")
	}
}

func TestSeaGlassUsageContrast(t *testing.T) {
	for _, percent := range []int{25, 65, 85, 95} {
		line := tmuxTheme(weeklyUsageTMUX(&UsageWindow{UsedPercent: percent}, time.Now()), "seaglass")
		if strings.Contains(line, "colour") || strings.Contains(line, ",dim") {
			t.Fatalf("usage inherited a dim or terminal-dependent style: %s", line)
		}
		if !strings.Contains(line, ",nodim,bold]") {
			t.Fatalf("usage lost emphasis: %s", line)
		}
	}
}

func TestParseAgentDeckEnvironmentDerivesThemeFromColorFGBG(t *testing.T) {
	if got := parseAgentDeckEnvironment("AGENTDECK_INSTANCE_ID=a\nCOLORFGBG=0;15\n").theme; got != "seaglass" {
		t.Fatalf("light COLORFGBG theme = %q, want seaglass", got)
	}
	if got := parseAgentDeckEnvironment("AGENTDECK_INSTANCE_ID=a\nCOLORFGBG=15;0\n").theme; got != "" {
		t.Fatalf("dark COLORFGBG theme = %q, want terminal palette", got)
	}
	if got := parseAgentDeckEnvironment("AGENTDECK_INSTANCE_ID=a\nCOLORFGBG=15;0\nCODEX_STATUSLINE_THEME=seaglass\n").theme; got != "seaglass" {
		t.Fatalf("explicit theme = %q, want seaglass", got)
	}
}
