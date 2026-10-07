package render

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Self-hosted CI containers on this host. They run in the idle CPU class, so
// their share is shown apart from the owner's: it yields the moment the owner
// needs the CPU and says nothing about room for new work.
const ciCPUStatGlob = "/sys/fs/cgroup/lxc.payload.ci-*/cpu.stat"

// USER_HZ, the unit of /proc/stat, is 100 on every Linux ABI Go supports.
const procStatUsecPerTick = 10000

type cpuSample struct {
	At    int64            `json:"at"`
	Total int64            `json:"total"`
	Idle  int64            `json:"idle"`
	CI    map[string]int64 `json:"ci"`
}

func readCPUSample(now time.Time) (cpuSample, bool) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return cpuSample{}, false
	}
	line, _, _ := strings.Cut(string(data), "\n")
	fields := strings.Fields(line)
	if len(fields) < 6 || fields[0] != "cpu" {
		return cpuSample{}, false
	}
	sample := cpuSample{At: now.UnixMilli(), CI: map[string]int64{}}
	for i, field := range fields[1:] {
		value, _ := strconv.ParseInt(field, 10, 64)
		sample.Total += value
		if i == 3 || i == 4 {
			sample.Idle += value
		}
	}
	paths, _ := filepath.Glob(ciCPUStatGlob)
	for _, path := range paths {
		if usec, ok := cgroupUsageUsec(path); ok {
			sample.CI[path] = usec
		}
	}
	return sample, true
}

func cgroupUsageUsec(path string) (int64, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if value, ok := strings.CutPrefix(line, "usage_usec "); ok {
			usec, err := strconv.ParseInt(value, 10, 64)
			return usec, err == nil
		}
	}
	return 0, false
}

// splitCPU returns the owner's and CI's share of all cores between two
// samples, in percent. A CI container absent from the earlier sample started
// inside the window, so all of its usage counts; one that has since vanished
// took its usage with it, which can only understate CI.
func splitCPU(prev, cur cpuSample) (owner, ci int) {
	ticks := cur.Total - prev.Total
	if ticks <= 0 {
		return 0, 0
	}
	busy := float64(ticks-(cur.Idle-prev.Idle)) * 100 / float64(ticks)
	ciUsec := int64(0)
	for path, usec := range cur.CI {
		if delta := usec - prev.CI[path]; delta > 0 {
			ciUsec += delta
		}
	}
	ciPct := float64(ciUsec) * 100 / float64(ticks*procStatUsecPerTick)
	ciPct = min(max(ciPct, 0), max(busy, 0))
	return int(max(busy-ciPct, 0) + 0.5), int(ciPct + 0.5)
}

// linuxCPU measures since the previous render from any session, stored under
// the runtime dir. Without a usable earlier sample it measures a short window.
func linuxCPU() (owner, ci int) {
	state := filepath.Join(cpuStateDir(), fmt.Sprintf("codex-statusline-cpu.%d.json", os.Getuid()))
	now := time.Now()
	cur, ok := readCPUSample(now)
	if !ok {
		return 0, 0
	}
	var prev cpuSample
	data, err := os.ReadFile(state)
	if err != nil || json.Unmarshal(data, &prev) != nil || now.UnixMilli()-prev.At < 1000 || cur.Total <= prev.Total {
		prev = cur
		time.Sleep(300 * time.Millisecond)
		if cur, ok = readCPUSample(time.Now()); !ok {
			return 0, 0
		}
	}
	if encoded, err := json.Marshal(cur); err == nil {
		tmp := fmt.Sprintf("%s.%d", state, os.Getpid())
		if os.WriteFile(tmp, encoded, 0o600) == nil {
			_ = os.Rename(tmp, state)
		}
	}
	return splitCPU(prev, cur)
}

func cpuStateDir() string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return dir
	}
	return os.TempDir()
}

// machineLabel renders "↯owner+ci ⛁mem"; the CI term is dropped when CI is
// idle, and memory turns alert-colored at 85% because nothing yields it.
func machineLabel(stats Stats, alert, normal string) string {
	cpu := fmt.Sprintf("↯%d", stats.CPU)
	if stats.CI > 0 {
		cpu += fmt.Sprintf("+%d", stats.CI)
	}
	mem := fmt.Sprintf("⛁%d", stats.Mem)
	if stats.Mem >= 85 && alert != "" {
		mem = alert + mem + normal
	}
	return cpu + " " + mem
}
