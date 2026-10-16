package render

import (
	"os"
	"path/filepath"
	"testing"
)

func keepTable(table map[string]float64) func(string) float64 {
	return func(name string) float64 { return table[name] }
}

func TestGuestSharesAndFreeThreads(t *testing.T) {
	// 24 cores for one second: 2400 ticks, 24e6 usec of guest-measurable time.
	prev := cpuSample{Guests: map[string]int64{"ci-1": 0, "la-casa": 0}}
	cur := cpuSample{Total: 2400, Idle: 240, Guests: map[string]int64{
		"ci-1":     12_000_000, // 50%, idle class: yields all of it
		"la-casa":  4_800_000,  // 20%, equal weight: yields half
		"casa-zoe": 2_400_000,  // 10%, started inside the window; weight 25: yields 80%
	}}
	keep := keepTable(map[string]float64{"ci-1": 0, "la-casa": 0.5, "casa-zoe": 0.2})
	idle, guests := guestShares(prev, cur, keep)
	if idle != 10 || len(guests) != 3 {
		t.Fatalf("idle, guests = %v, %v", idle, guests)
	}
	// 10 idle + 50 + 10 + 8 = 78% of 24 threads.
	if got := freeThreads(idle, guests, 24); got != 19 {
		t.Fatalf("freeThreads = %d; want 19", got)
	}
	if got := culprit(guests); got != "la-casa" {
		t.Fatalf("culprit = %q; want la-casa", got)
	}
}

func TestGuestSharesNeverExceedBusy(t *testing.T) {
	prev := cpuSample{Guests: map[string]int64{}}
	cur := cpuSample{Total: 100, Idle: 50, Guests: map[string]int64{"ci-1": 900_000}}
	idle, guests := guestShares(prev, cur, keepTable(nil))
	if idle != 50 || guests[0].used != 50 {
		t.Fatalf("idle, used = %v, %v; want 50, 50", idle, guests[0].used)
	}
	if got := freeThreads(idle, guests, 24); got != 24 {
		t.Fatalf("freeThreads = %d; want 24", got)
	}
}

func TestGuestSharesWithoutTicks(t *testing.T) {
	sample := cpuSample{Total: 5, Idle: 5}
	if idle, guests := guestShares(sample, sample, keepTable(nil)); idle != 0 || guests != nil {
		t.Fatalf("idle, guests = %v, %v; want 0, nil", idle, guests)
	}
}

func TestCulpritNeedsAMeaningfulHold(t *testing.T) {
	guests := []guestShare{{name: "ci-1", used: 80, keep: 0}, {name: "casa-zoe", used: 10, keep: 0.2}}
	if got := culprit(guests); got != "" {
		t.Fatalf("culprit = %q; want none", got)
	}
}

func TestPressureAvg10(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cpu.pressure")
	data := "some avg10=6.60 avg60=5.14 avg300=4.70 total=2833174632\nfull avg10=5.74 avg60=4.35 avg300=3.95 total=1\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, ok := pressureAvg10(path); !ok || got != 7 {
		t.Fatalf("pressureAvg10 = %d, %v; want 7, true", got, ok)
	}
}

func TestMachineLabel(t *testing.T) {
	color := func(severity int) string {
		if severity >= severityHigh {
			return "<"
		}
		return "{"
	}
	host := func(load HostLoad) Stats { return Stats{Host: &load} }
	cases := []struct {
		stats Stats
		want  string
	}{
		{Stats{CPU: 12, Mem: 45}, "↯12 ⛁45"},
		{Stats{CPU: 3, Mem: 90}, "↯3 <⛁90>"},
		{host(HostLoad{FreeThreads: 16, Wait: 2, MemFreeGiB: 98, PoquitoWait: -1}), "↯16t ◷2% ⛁98G"},
		{host(HostLoad{FreeThreads: 16, Wait: 6, MemFreeGiB: 98, PoquitoWait: 3}), "↯16t {◷6%> ⛁98G"},
		{host(HostLoad{FreeThreads: 2, Wait: 24, Culprit: "la-casa", MemFreeGiB: 30, PoquitoWait: 22}),
			"↯2t <◷24% la-casa> {⌂22%> {⛁30G>"},
		{host(HostLoad{FreeThreads: 9, MemFreeGiB: 12, PoquitoWait: 15}), "↯9t ◷0% {⌂15%> <⛁12G>"},
	}
	for _, c := range cases {
		if got := machineLabel(c.stats, color, ">"); got != c.want {
			t.Errorf("machineLabel(%+v) = %q; want %q", c.stats.Host, got, c.want)
		}
	}
	if got := machineLabel(cases[4].stats, nil, ""); got != "↯2t ◷24% la-casa ⌂22% ⛁30G" {
		t.Errorf("plain machineLabel = %q", got)
	}
}
