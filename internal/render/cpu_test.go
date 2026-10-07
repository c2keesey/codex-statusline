package render

import "testing"

func TestSplitCPUSeparatesIdleClassCI(t *testing.T) {
	// 24 cores for one second: 2400 ticks, 24e6 CI-measurable usec.
	prev := cpuSample{Total: 0, Idle: 0, CI: map[string]int64{"a": 0}}
	cur := cpuSample{Total: 2400, Idle: 240, CI: map[string]int64{"a": 18_000_000, "b": 1_200_000}}
	owner, ci := splitCPU(prev, cur)
	if owner != 10 || ci != 80 {
		t.Fatalf("owner, ci = %d, %d; want 10, 80", owner, ci)
	}
}

func TestSplitCPUNeverExceedsBusy(t *testing.T) {
	prev := cpuSample{CI: map[string]int64{}}
	cur := cpuSample{Total: 100, Idle: 50, CI: map[string]int64{"a": 900_000}}
	owner, ci := splitCPU(prev, cur)
	if owner != 0 || ci != 50 {
		t.Fatalf("owner, ci = %d, %d; want 0, 50", owner, ci)
	}
}

func TestSplitCPUWithoutTicks(t *testing.T) {
	sample := cpuSample{Total: 5, Idle: 5}
	if owner, ci := splitCPU(sample, sample); owner != 0 || ci != 0 {
		t.Fatalf("owner, ci = %d, %d; want 0, 0", owner, ci)
	}
}

func TestMachineLabel(t *testing.T) {
	cases := []struct {
		stats Stats
		want  string
	}{
		{Stats{CPU: 12, Mem: 45}, "↯12 ⛁45"},
		{Stats{CPU: 12, CI: 80, Mem: 45}, "↯12+80 ⛁45"},
		{Stats{CPU: 3, Mem: 90}, "↯3 <⛁90>"},
	}
	for _, c := range cases {
		if got := machineLabel(c.stats, "<", ">"); got != c.want {
			t.Errorf("machineLabel(%+v) = %q; want %q", c.stats, got, c.want)
		}
	}
}
