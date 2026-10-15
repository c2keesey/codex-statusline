package render

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func quotaReport(percent int, reset int64) string {
	return fmt.Sprintf(`{"providers":[{"id":"codex","windows":[{"kind":"seven_day","used_percentage":%d,"resets_at":%d}]}]}`, percent, reset)
}

func seedUsageCache(t *testing.T, fetched, attempted time.Time, percent int, reset int64) string {
	t.Helper()
	path := filepath.Join(os.TempDir(), "codex-statusline-usage.json")
	cache := usageCache{
		FetchedAt: fetched.Unix(), LastAttempt: attempted.Unix(),
		Usage: Usage{SevenDay: &UsageWindow{UsedPercent: percent, ResetsAt: reset, WindowDurationMins: sevenDayWindowMins}},
	}
	if err := writeUsageCache(path, cache); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestUsageRatesRefreshesExpiredWindowBeforeCacheTTL(t *testing.T) {
	now := time.Now()
	fakeAgentDeck(t, quotaReport(0, now.Add(7*24*time.Hour).Unix()))
	seedUsageCache(t, now, now.Add(-usageRetryDelay-time.Second), 56, now.Add(-time.Second).Unix())
	// A normal agent-deck read can still serve its own cached pre-reset window.
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$3\" != '--refresh' ]; then exit 1; fi\nprintf '%%s\\n' '%s'\n", quotaReport(0, now.Add(7*24*time.Hour).Unix()))
	if err := os.WriteFile(filepath.Join(os.Getenv("PATH"), "agent-deck"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := UsageRates(); got.SevenDay == nil || got.SevenDay.UsedPercent != 0 {
		t.Fatalf("usage after reset = %#v", got.SevenDay)
	}
}

func TestUsageRatesRefreshesExpiredProviderSnapshot(t *testing.T) {
	now := time.Now()
	fakeAgentDeck(t, "stub")
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$3\" = '--refresh' ]; then\nprintf '%%s\\n' '%s'\nelse\nprintf '%%s\\n' '%s'\nfi\n", quotaReport(0, now.Add(7*24*time.Hour).Unix()), quotaReport(56, now.Add(-time.Second).Unix()))
	if err := os.WriteFile(filepath.Join(os.Getenv("PATH"), "agent-deck"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := UsageRates(); got.SevenDay == nil || got.SevenDay.UsedPercent != 0 {
		t.Fatalf("usage after provider refresh = %#v", got.SevenDay)
	}
}

func TestUsageRatesDoesNotPresentExpiredFallback(t *testing.T) {
	for _, mode := range []string{"fetch failure", "retry delay", "locked", "expired provider"} {
		t.Run(mode, func(t *testing.T) {
			now := time.Now()
			output := ""
			if mode == "expired provider" {
				output = quotaReport(56, now.Add(-time.Second).Unix())
			}
			fakeAgentDeck(t, output)
			attempted := now.Add(-usageRetryDelay - time.Second)
			if mode == "retry delay" {
				attempted = now
			}
			path := seedUsageCache(t, now, attempted, 56, now.Add(-time.Second).Unix())
			if mode == "locked" {
				if err := os.WriteFile(path+".lock", nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if got := UsageRates(); got.SevenDay != nil {
				t.Fatalf("expired usage rendered as current: %#v", got.SevenDay)
			}
		})
	}
}

func TestUsageRatesAcceptsUnscheduledReset(t *testing.T) {
	now := time.Now()
	reset := now.Add(5 * 24 * time.Hour).Unix()
	fakeAgentDeck(t, quotaReport(0, reset))
	seedUsageCache(t, now.Add(-usageCacheTTL-time.Second), now.Add(-usageCacheTTL-time.Second), 56, reset)
	if got := UsageRates(); got.SevenDay == nil || got.SevenDay.UsedPercent != 0 {
		t.Fatalf("usage after unscheduled reset = %#v", got.SevenDay)
	}
}

func TestUsageRatesPreservesUnexpiredFallbackOnFailure(t *testing.T) {
	now := time.Now()
	fakeAgentDeck(t, "")
	seedUsageCache(t, now.Add(-usageCacheTTL-time.Second), now.Add(-usageCacheTTL-time.Second), 56, now.Add(time.Hour).Unix())
	if got := UsageRates(); got.SevenDay == nil || got.SevenDay.UsedPercent != 56 {
		t.Fatalf("last known current-window usage = %#v", got.SevenDay)
	}
}

func TestUsageCurrentExpiresAtResetBoundary(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	usage := Usage{SevenDay: &UsageWindow{UsedPercent: 56, ResetsAt: now.Unix()}}
	if usage.current(now.Add(-time.Second)).SevenDay == nil {
		t.Fatal("window expired before its reset")
	}
	if got := weeklyUsage(usage.current(now).SevenDay, now); !strings.HasSuffix(got, " —") {
		t.Fatalf("expired window = %q", got)
	}
}
