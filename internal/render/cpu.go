package render

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// HostLoad is the shared host as its owner experiences it: room left, time
// spent waiting for a CPU, and memory left. Every other workload on this host
// is an Incus container, so "the owner" is everything outside them.
type HostLoad struct {
	// FreeThreads is how many threads the owner could take now: idle ones
	// plus what containers would hand back under contention.
	FreeThreads int
	// Wait is the share of time the owner's runnable tasks spent waiting for
	// a CPU over the last 10s (PSI "some" avg10), in percent.
	Wait int
	// Culprit is the container keeping the most CPU from the owner, set only
	// while Wait is high enough to show it.
	Culprit    string
	MemFreeGiB int
	// PoquitoWait is la-casa's own CPU wait, or -1 without a la-casa.
	PoquitoWait int
}

const (
	guestCgroupGlob = "/sys/fs/cgroup/lxc.payload.*"
	guestPrefix     = "lxc.payload."
	poquitoGuest    = "la-casa"
	// USER_HZ, the unit of /proc/stat, is 100 on every Linux ABI Go supports.
	procStatUsecPerTick = 10000

	waitWarn     = 5
	waitAlert    = 20
	culpritMin   = 5
	poquitoShow  = 15
	memAlertGiB  = 20
	memWarnGiB   = 40
	severityWarn = 60
	severityHigh = 90
)

type cpuSample struct {
	At     int64            `json:"at"`
	Total  int64            `json:"total"`
	Idle   int64            `json:"idle"`
	Guests map[string]int64 `json:"guests"`
}

// guestShare is one container between two samples: its share of all cores
// and the fraction of that it keeps when the owner competes for the CPU.
type guestShare struct {
	name string
	used float64
	keep float64
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
	sample := cpuSample{At: now.UnixMilli(), Guests: map[string]int64{}}
	for i, field := range fields[1:] {
		value, _ := strconv.ParseInt(field, 10, 64)
		sample.Total += value
		if i == 3 || i == 4 {
			sample.Idle += value
		}
	}
	dirs, _ := filepath.Glob(guestCgroupGlob)
	for _, dir := range dirs {
		if usec, ok := cgroupUsageUsec(filepath.Join(dir, "cpu.stat")); ok {
			sample.Guests[strings.TrimPrefix(filepath.Base(dir), guestPrefix)] = usec
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

func readInt(path string) (int, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	value, err := strconv.Atoi(strings.TrimSpace(string(data)))
	return value, err == nil
}

// keepFraction is what a container holds on to when the owner's slice wants
// the CPU too: cpu.weight splits contended time proportionally, and an
// idle-class container (cpu.idle, the self-hosted CI) yields all of it.
func keepFraction(guest string, ownerWeight int) float64 {
	dir := filepath.Join("/sys/fs/cgroup", guestPrefix+guest)
	if idle, ok := readInt(filepath.Join(dir, "cpu.idle")); ok && idle == 1 {
		return 0
	}
	weight, ok := readInt(filepath.Join(dir, "cpu.weight"))
	if !ok || weight+ownerWeight <= 0 {
		return 1
	}
	return float64(weight) / float64(weight+ownerWeight)
}

// guestShares returns each container's share of all cores between two
// samples. A container absent from the earlier sample started inside the
// window, so all of its usage counts; one that has since vanished took its
// usage with it, which can only understate the guests.
func guestShares(prev, cur cpuSample, keep func(string) float64) (idle float64, guests []guestShare) {
	ticks := cur.Total - prev.Total
	if ticks <= 0 {
		return 0, nil
	}
	idle = float64(cur.Idle-prev.Idle) * 100 / float64(ticks)
	busy := 100 - idle
	for name, usec := range cur.Guests {
		delta := usec - prev.Guests[name]
		if delta <= 0 {
			continue
		}
		used := min(float64(delta)*100/float64(ticks*procStatUsecPerTick), busy)
		guests = append(guests, guestShare{name: name, used: used, keep: keep(name)})
	}
	sort.Slice(guests, func(i, j int) bool { return guests[i].name < guests[j].name })
	return idle, guests
}

// freeThreads counts idle threads plus what containers would yield.
func freeThreads(idle float64, guests []guestShare, cores int) int {
	free := idle
	for _, g := range guests {
		free += g.used * (1 - g.keep)
	}
	return min(int(min(free, 100)*float64(cores)/100+0.5), cores)
}

// culprit names the container keeping the most CPU from the owner, if any
// keeps enough to matter.
func culprit(guests []guestShare) string {
	best, kept := "", float64(culpritMin)
	for _, g := range guests {
		if held := g.used * g.keep; held >= kept {
			best, kept = g.name, held
		}
	}
	return best
}

// pressureAvg10 reads the "some avg10" of a PSI file, rounded to a percent.
func pressureAvg10(path string) (int, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(line, "some "); ok {
			for _, field := range strings.Fields(rest) {
				if value, ok := strings.CutPrefix(field, "avg10="); ok {
					avg, err := strconv.ParseFloat(value, 64)
					return int(avg + 0.5), err == nil
				}
			}
		}
	}
	return 0, false
}

// sampleCPU measures since the previous render from any session, stored
// under the runtime dir. Without a usable earlier sample it measures a short
// window.
func sampleCPU() (prev, cur cpuSample, ok bool) {
	state := filepath.Join(cpuStateDir(), fmt.Sprintf("codex-statusline-cpu.%d.json", os.Getuid()))
	now := time.Now()
	if cur, ok = readCPUSample(now); !ok {
		return prev, cur, false
	}
	data, err := os.ReadFile(state)
	if err != nil || json.Unmarshal(data, &prev) != nil || prev.Guests == nil ||
		now.UnixMilli()-prev.At < 1000 || cur.Total <= prev.Total {
		prev = cur
		time.Sleep(300 * time.Millisecond)
		if cur, ok = readCPUSample(time.Now()); !ok {
			return prev, cur, false
		}
	}
	if encoded, err := json.Marshal(cur); err == nil {
		tmp := fmt.Sprintf("%s.%d", state, os.Getpid())
		if os.WriteFile(tmp, encoded, 0o600) == nil {
			_ = os.Rename(tmp, state)
		}
	}
	return prev, cur, true
}

func cpuStateDir() string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return dir
	}
	return os.TempDir()
}

func memAvailableGiB() int {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && fields[0] == "MemAvailable:" {
			kib, _ := strconv.ParseInt(fields[1], 10, 64)
			return int(kib >> 20)
		}
	}
	return 0
}

func linuxStats() Stats {
	ownerSlice := fmt.Sprintf("/sys/fs/cgroup/user.slice/user-%d.slice", os.Getuid())
	ownerWeight, ok := readInt(filepath.Join(ownerSlice, "cpu.weight"))
	if !ok {
		ownerWeight = 100
	}
	load := HostLoad{MemFreeGiB: memAvailableGiB(), PoquitoWait: -1}
	load.Wait, _ = pressureAvg10(filepath.Join(ownerSlice, "cpu.pressure"))
	if prev, cur, ok := sampleCPU(); ok {
		idle, guests := guestShares(prev, cur, func(name string) float64 { return keepFraction(name, ownerWeight) })
		load.FreeThreads = freeThreads(idle, guests, runtime.NumCPU())
		if load.Wait >= waitAlert {
			load.Culprit = culprit(guests)
		}
	}
	if wait, ok := pressureAvg10(filepath.Join("/sys/fs/cgroup", guestPrefix+poquitoGuest, "cpu.pressure")); ok {
		load.PoquitoWait = wait
	}
	return Stats{Host: &load}
}

// machineLabel renders the machine group. color maps a 0-100 severity to an
// escape and normal restores the group's own color; a nil color renders
// plain text.
//
// Linux: "↯16t ◷6% ⛁98G", plus the culprit after a high wait ("◷24% la-casa")
// and "⌂22%" while Poquito's own wait is high. macOS: "↯12 ⛁45".
func machineLabel(stats Stats, color func(int) string, normal string) string {
	paint := func(severity int, text string) string {
		if color == nil || severity < severityWarn {
			return text
		}
		return color(severity) + text + normal
	}
	load := stats.Host
	if load == nil {
		mem := fmt.Sprintf("⛁%d", stats.Mem)
		if stats.Mem >= 85 {
			mem = paint(severityHigh, mem)
		}
		return fmt.Sprintf("↯%d %s", stats.CPU, mem)
	}
	wait := fmt.Sprintf("◷%d%%", load.Wait)
	if load.Culprit != "" {
		wait += " " + load.Culprit
	}
	switch {
	case load.Wait >= waitAlert:
		wait = paint(severityHigh, wait)
	case load.Wait >= waitWarn:
		wait = paint(severityWarn, wait)
	}
	mem := fmt.Sprintf("⛁%dG", load.MemFreeGiB)
	switch {
	case load.MemFreeGiB < memAlertGiB:
		mem = paint(severityHigh, mem)
	case load.MemFreeGiB < memWarnGiB:
		mem = paint(severityWarn, mem)
	}
	parts := []string{fmt.Sprintf("↯%dt", load.FreeThreads), wait}
	if load.PoquitoWait >= poquitoShow {
		parts = append(parts, paint(severityWarn, fmt.Sprintf("⌂%d%%", load.PoquitoWait)))
	}
	return strings.Join(append(parts, mem), " ")
}

// The machine group's alert colors are plain weight; the usage gauges keep
// their bold.
func machineColour(severity int) int {
	if severity >= severityHigh {
		return 9
	}
	return 136
}

func ansiMachineColor(severity int) string {
	return fmt.Sprintf("\x1b[38;5;%dm", machineColour(severity))
}

func tmuxMachineColor(severity int) string {
	return fmt.Sprintf("#[fg=colour%d]", machineColour(severity))
}
