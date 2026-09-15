package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
	if metadata.instanceID != "abc-123" || !metadata.codex {
		t.Fatalf("unexpected metadata: %#v", metadata)
	}
	metadata = parseAgentDeckEnvironment("AGENTDECK_INSTANCE_ID=abc-123\nCLAUDE_SESSION_ID=claude-456\n")
	if metadata.instanceID != "abc-123" || metadata.codex {
		t.Fatalf("unexpected Claude metadata: %#v", metadata)
	}
}

func TestParseNativeContextPercentUsesVisibleFooter(t *testing.T) {
	input := "old transcript text: Context 85% used\n\n  wt/gentle-salmon · Context 22% used · weekly 90% left\n"
	got, ok := parseNativeContextPercent(input)
	if !ok || got != 22 {
		t.Fatalf("got %d, ok %v; want 22, true", got, ok)
	}
}

func TestParseNativeContextPercentClampsCodexOverflow(t *testing.T) {
	got, ok := parseNativeContextPercent("Context 101% used")
	if !ok || got != 100 {
		t.Fatalf("got %d, ok %v; want 100, true", got, ok)
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

func TestClassifyUsageByWindowDuration(t *testing.T) {
	five, seven, reset := int64(300), int64(10080), int64(2_000_000)
	got := classifyUsage(rateLimitSnapshot{
		Primary:   &rateLimitWindow{UsedPercent: 12, WindowDurationMins: &five},
		Secondary: &rateLimitWindow{UsedPercent: 34, WindowDurationMins: &seven, ResetsAt: &reset},
	})
	if got.SevenDay == nil || got.SevenDay.UsedPercent != 34 || got.SevenDay.ResetsAt != reset {
		t.Fatalf("unexpected seven-day usage: %#v", got.SevenDay)
	}
}

func TestBurnRatio(t *testing.T) {
	end := time.Unix(2_000_000, 0)
	window := &UsageWindow{UsedPercent: 30, WindowDurationMins: 10080, ResetsAt: end.Unix()}
	now := end.Add(-7 * 24 * time.Hour / 2)
	ratio, ok := burnRatio(window, now)
	if !ok || ratio < 0.599 || ratio > 0.601 {
		t.Fatalf("got ratio %v, ok %v", ratio, ok)
	}
	if got := weeklyUsage(window, now); got != "7d ▰▱▱▱ 30% ⇡0.6×" {
		t.Fatalf("got %q", got)
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

func TestBurnRatioHiddenAtWindowStart(t *testing.T) {
	end := time.Unix(2_000_000, 0)
	window := &UsageWindow{UsedPercent: 1, WindowDurationMins: 10080, ResetsAt: end.Unix()}
	if _, ok := burnRatio(window, end.Add(-7*24*time.Hour+time.Hour)); ok {
		t.Fatal("expected ratio to be hidden before 2% of the window elapsed")
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
