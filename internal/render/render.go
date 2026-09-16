package render

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type Stats struct {
	CPU int
	Mem int
}

type Usage struct {
	SevenDay *UsageWindow `json:"seven_day,omitempty"`
}

type UsageWindow struct {
	UsedPercent        int   `json:"used_percent"`
	WindowDurationMins int64 `json:"window_duration_mins"`
	ResetsAt           int64 `json:"resets_at"`
}

type usageCache struct {
	FetchedAt   int64 `json:"fetched_at"`
	LastAttempt int64 `json:"last_attempt,omitempty"`
	Usage
}

type paceSchedule struct {
	weekdays map[string]float64
	dates    map[string]float64
}

type rateLimitWindow struct {
	UsedPercent        int    `json:"usedPercent"`
	WindowDurationMins *int64 `json:"windowDurationMins"`
	ResetsAt           *int64 `json:"resetsAt"`
}

type rateLimitSnapshot struct {
	Primary   *rateLimitWindow `json:"primary"`
	Secondary *rateLimitWindow `json:"secondary"`
}

var agentDeckSessionPattern = regexp.MustCompile(`^agentdeck_(.+)_([[:xdigit:]]{8})$`)
var nativeContextPattern = regexp.MustCompile(`\bContext ([0-9]+)% used\b`)

const (
	usageCacheTTL   = 30 * time.Second
	usageRetryDelay = 10 * time.Second
	usageLockMaxAge = 8 * time.Second
)

func SystemStats() Stats {
	if runtime.GOOS == "darwin" {
		return darwinStats()
	}
	return linuxStats()
}

func Line(cwd string) string {
	return LineWithSession(cwd, "")
}

func LineWithSession(cwd, session string) string {
	stats := SystemStats()
	groups := make([]string, 0, 4)
	if value, ok := ContextPercent(session); ok {
		groups = append(groups, fmt.Sprintf("%d%%", value))
	}
	usage := UsageRates()
	groups = append(groups, weeklyUsage(usage.SevenDay, time.Now()))
	machine := []string{fmt.Sprintf("↯%d", stats.CPU), fmt.Sprintf("⛁%d", stats.Mem)}
	groups = append(groups, strings.Join(machine, " "))
	if label := CompactSession(session); label != "" {
		groups = append(groups, label)
	}
	return strings.Join(groups, " │ ")
}

const (
	tmuxReset        = "#[default]"
	tmuxGray         = "#[fg=default,dim]"
	tmuxDimGreen     = "#[fg=colour2]"
	tmuxBarEmpty     = "#[fg=default,dim]"
	tmuxSummaryColor = "#[fg=colour12]"
)

// LineWithSessionTMUX mirrors the color roles in claude-statusline: percentage
// gauges use the green-to-red ramp, system stats are dark green, separators are
// gray, and the session summary is soft blue.
func LineWithSessionTMUX(cwd, session string) string {
	metadata, ok := agentDeckSessionMetadata(session)
	if !ok || !metadata.codex {
		return ""
	}
	stats := SystemStats()
	groups := make([]string, 0, 4)
	if value, ok := contextPercentForSession(session, metadata.instanceID); ok {
		groups = append(groups, tmuxPercentColor(value)+fmt.Sprintf("%d%%", value)+tmuxReset)
	}
	usage := UsageRates()
	groups = append(groups, weeklyUsageTMUX(usage.SevenDay, time.Now()))
	machine := fmt.Sprintf("%s↯%d ⛁%d", tmuxDimGreen, stats.CPU, stats.Mem)
	groups = append(groups, machine+tmuxReset)
	if label := CompactSession(session); label != "" {
		groups = append(groups, tmuxSummaryColor+label+tmuxReset)
	}
	return tmuxTheme(strings.Join(groups, " "+tmuxGray+"│"+tmuxReset+" "), metadata.theme)
}

func weeklyUsageTMUX(window *UsageWindow, now time.Time) string {
	if window == nil {
		return tmuxGray + "7d " + tmuxBarEmpty + "▱▱▱▱" + tmuxGray + " —" + tmuxReset
	}
	percentColor := tmuxPercentColor(window.UsedPercent)
	filled := int(float64(window.UsedPercent)/25 + 0.5)
	if filled < 0 {
		filled = 0
	}
	if filled > 4 {
		filled = 4
	}
	bar := percentColor + strings.Repeat("▰", filled)
	if filled < 4 {
		bar += tmuxBarEmpty + strings.Repeat("▱", 4-filled)
	}
	label := tmuxGray + "7d " + bar + " " + percentColor + fmt.Sprintf("%d%%", window.UsedPercent) + tmuxReset
	if ratio, ok := burnRatio(window, now); ok {
		severity := int(ratio / 1.5 * 100)
		label += " " + tmuxPercentColor(severity) + "⇡" + formatRatio(ratio) + "×" + tmuxReset
	}
	return label
}

func tmuxPercentColor(percent int) string {
	colour := 10 // green; follows the active terminal palette
	switch {
	case percent >= 90:
		colour = 9 // red
	case percent >= 80:
		colour = 166 // orange with contrast on light and dark backgrounds
	case percent >= 60:
		colour = 136 // gold with contrast on light and dark backgrounds
	}
	return fmt.Sprintf("#[fg=colour%d,nodim,bold]", colour)
}

// Sea Glass uses ocean ink on pale seafoam. Explicit RGB colors keep usage
// legible even when the terminal's bright ANSI colors target a dark background.
func tmuxTheme(line, theme string) string {
	if theme != "seaglass" {
		return line
	}
	return strings.NewReplacer(
		"fg=colour10", "fg=#0F7B58",
		"fg=colour136", "fg=#805B18",
		"fg=colour166", "fg=#A44716",
		"fg=colour9", "fg=#B2302D",
		"fg=colour2]", "fg=#42665E]",
		"fg=colour12", "fg=#18708F",
		"fg=default,dim", "fg=#5E7370,nodim,nobold",
	).Replace(line)
}

type tokenCountEvent struct {
	Type    string `json:"type"`
	Payload struct {
		Type string `json:"type"`
		Item struct {
			Type string `json:"type"`
		} `json:"item"`
		Info struct {
			LastTokenUsage struct {
				TotalTokens int64 `json:"total_tokens"`
			} `json:"last_token_usage"`
			ModelContextWindow int64 `json:"model_context_window"`
		} `json:"info"`
	} `json:"payload"`
}

type agentDeckMetadata struct {
	instanceID string
	codex      bool
	theme      string
}

func agentDeckSessionMetadata(session string) (agentDeckMetadata, bool) {
	if session == "" {
		return agentDeckMetadata{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tmux", "show-environment", "-t", session).Output()
	if err != nil {
		return agentDeckMetadata{}, false
	}
	metadata := parseAgentDeckEnvironment(string(out))
	return metadata, metadata.instanceID != ""
}

func parseAgentDeckEnvironment(input string) agentDeckMetadata {
	metadata := agentDeckMetadata{}
	for line := range strings.SplitSeq(input, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "AGENTDECK_INSTANCE_ID":
			metadata.instanceID = strings.TrimSpace(value)
		case "CODEX_STATUSLINE_THEME":
			metadata.theme = strings.TrimSpace(value)
		case "CODEX_SESSION_ID":
			metadata.codex = strings.TrimSpace(value) != ""
		}
	}
	return metadata
}

// ContextPercent reads the latest context count from the Codex rollout that
// belongs to an Agent Deck session. Codex reports cumulative thread usage as
// well, but the last request size is the value currently occupying context.
func ContextPercent(session string) (int, bool) {
	metadata, ok := agentDeckSessionMetadata(session)
	if !ok || !metadata.codex {
		return 0, false
	}
	return contextPercentForSession(session, metadata.instanceID)
}

func contextPercentForSession(session, instanceID string) (int, bool) {
	if value, ok := nativeContextPercent(session); ok {
		return value, true
	}
	return contextPercentForInstance(instanceID)
}

func nativeContextPercent(session string) (int, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tmux", "capture-pane", "-p", "-t", session).Output()
	if err != nil {
		return 0, false
	}
	return parseNativeContextPercent(string(out))
}

func parseNativeContextPercent(input string) (int, bool) {
	matches := nativeContextPattern.FindAllStringSubmatch(input, -1)
	if len(matches) == 0 {
		return 0, false
	}
	value, err := strconv.Atoi(matches[len(matches)-1][1])
	if err != nil || value < 0 {
		return 0, false
	}
	if value > 100 {
		value = 100
	}
	return value, true
}

func contextPercentForInstance(instanceID string) (int, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "agent-deck", "session", "transcript", instanceID).Output()
	if err != nil {
		return 0, false
	}
	return contextPercentFromRollout(strings.TrimSpace(string(out)))
}

func contextPercentFromRollout(path string) (int, bool) {
	file, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer file.Close()

	const tailBytes int64 = 256 * 1024
	if info, statErr := file.Stat(); statErr == nil && info.Size() > tailBytes {
		if _, err := file.Seek(-tailBytes, 2); err != nil {
			return 0, false
		}
		reader := bufio.NewReader(file)
		_, _ = reader.ReadString('\n')
		return scanContextPercent(reader)
	}
	return scanContextPercent(file)
}

func scanContextPercent(reader io.Reader) (int, bool) {
	const baselineTokens int64 = 12_000
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	value, found := 0, false
	for scanner.Scan() {
		var event tokenCountEvent
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		if event.Type == "compacted" || (event.Type == "event_msg" && event.Payload.Type == "item_completed" && event.Payload.Item.Type == "ContextCompaction") {
			value, found = 0, true
			continue
		}
		if event.Type != "event_msg" || event.Payload.Type != "token_count" {
			continue
		}
		used := event.Payload.Info.LastTokenUsage.TotalTokens
		window := event.Payload.Info.ModelContextWindow
		effectiveWindow := window - baselineTokens
		if used < 0 || effectiveWindow <= 0 {
			continue
		}
		used = max(used-baselineTokens, 0)
		remaining := max(effectiveWindow-used, 0)
		value = 100 - percent(remaining, effectiveWindow)
		if value < 0 {
			value = 0
		}
		found = true
	}
	return value, found
}

func CompactSession(session string) string {
	match := agentDeckSessionPattern.FindStringSubmatch(session)
	if len(match) != 3 {
		return strings.TrimPrefix(session, "agentdeck_")
	}
	return match[1] + "_" + match[2][:3]
}

func weeklyUsage(window *UsageWindow, now time.Time) string {
	if window == nil {
		return "7d ▱▱▱▱ —"
	}
	label := fmt.Sprintf("7d %s %d%%", usageGauge(window.UsedPercent), window.UsedPercent)
	if ratio, ok := burnRatio(window, now); ok {
		label += " ⇡" + formatRatio(ratio) + "×"
	}
	return label
}

// usageGauge mirrors the four-cell gauge in the Claude Code status line.
func usageGauge(percent int) string {
	filled := int(float64(percent)/25 + 0.5)
	if filled < 0 {
		filled = 0
	}
	if filled > 4 {
		filled = 4
	}
	return strings.Repeat("▰", filled) + strings.Repeat("▱", 4-filled)
}

// burnRatio is actual spend divided by expected spend at this point in the
// rolling window. A value of 1.0 is exactly on pace; 1.4 is burning 40% hot.
func burnRatio(window *UsageWindow, now time.Time) (float64, bool) {
	return burnRatioWithSchedule(window, now, loadPaceSchedule())
}

func burnRatioWithSchedule(window *UsageWindow, now time.Time, schedule paceSchedule) (float64, bool) {
	if window == nil || window.WindowDurationMins <= 0 || window.ResetsAt <= 0 {
		return 0, false
	}
	end := time.Unix(window.ResetsAt, 0).In(now.Location())
	start := end.Add(-time.Duration(window.WindowDurationMins) * time.Minute)
	if !now.After(start) {
		return 0, false
	}
	if now.After(end) {
		now = end
	}
	expectedFraction, ok := workweekExpected(start, end, now, schedule)
	if !ok {
		return 0, false
	}
	expectedPercent := expectedFraction * 100
	if expectedPercent < 2 {
		return 0, false
	}
	return float64(window.UsedPercent) / expectedPercent, true
}

func workweekExpected(start, end, now time.Time, schedule paceSchedule) (float64, bool) {
	if !end.After(start) {
		return 0, false
	}
	if now.Before(start) {
		now = start
	}
	if now.After(end) {
		now = end
	}

	var total, elapsed float64
	for dayStart := start; dayStart.Before(end); dayStart = dayStart.Add(24 * time.Hour) {
		dayEnd := dayStart.Add(24 * time.Hour)
		if dayEnd.After(end) {
			dayEnd = end
		}
		weight := schedule.weight(dayStart)
		total += weight * dayEnd.Sub(dayStart).Hours() / 24
		if now.After(dayStart) {
			elapsedEnd := now
			if elapsedEnd.After(dayEnd) {
				elapsedEnd = dayEnd
			}
			elapsed += weight * elapsedEnd.Sub(dayStart).Hours() / 24
		}
	}
	if total <= 0 {
		return 0, false
	}
	return elapsed / total, true
}

func (schedule paceSchedule) weight(day time.Time) float64 {
	weight := 1.0
	if day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
		weight = 0.5
	}
	weekday := strings.ToLower(day.Weekday().String()[:3])
	if override, ok := schedule.weekdays[weekday]; ok {
		weight = override
	}
	if override, ok := schedule.dates[day.Format("2006-01-02")]; ok {
		weight = override
	}
	return weight
}

func loadPaceSchedule() paceSchedule {
	path := os.Getenv("CODEX_STATUSLINE_SCHEDULE")
	if path == "" {
		path = os.Getenv("CLAUDE_STATUSLINE_SCHEDULE")
	}
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return paceSchedule{}
		}
		path = filepath.Join(home, ".claude", "statusline-schedule")
	}
	file, err := os.Open(path)
	if err != nil {
		return paceSchedule{}
	}
	defer file.Close()
	return parsePaceSchedule(file)
}

func parsePaceSchedule(reader io.Reader) paceSchedule {
	schedule := paceSchedule{
		weekdays: make(map[string]float64),
		dates:    make(map[string]float64),
	}
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := strings.SplitN(scanner.Text(), "#", 2)[0]
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		key := strings.ToLower(fields[0])
		weight := 0.0
		if len(fields) >= 2 {
			weight, _ = strconv.ParseFloat(fields[1], 64)
		}
		if len(key) == 3 && strings.Contains(" mon tue wed thu fri sat sun ", " "+key+" ") {
			if _, exists := schedule.weekdays[key]; !exists {
				schedule.weekdays[key] = weight
			}
			continue
		}
		if _, err := time.Parse("2006-01-02", key); err == nil {
			if _, exists := schedule.dates[key]; !exists {
				schedule.dates[key] = weight
			}
		}
	}
	return schedule
}

func formatRatio(ratio float64) string {
	switch {
	case ratio >= 10:
		return fmt.Sprintf("%.0f", ratio)
	case ratio >= 0.1:
		return fmt.Sprintf("%.1f", ratio)
	default:
		return fmt.Sprintf("%.2f", ratio)
	}
}

func UsageRates() Usage {
	cachePath := filepath.Join(os.TempDir(), "codex-statusline-usage.json")
	cached, cacheOK := readUsageCache(cachePath)
	now := time.Now()
	if cacheOK && cached.FetchedAt > 0 && now.Sub(time.Unix(cached.FetchedAt, 0)) < usageCacheTTL {
		return cached.Usage
	}
	if cacheOK && cached.LastAttempt > 0 && now.Sub(time.Unix(cached.LastAttempt, 0)) < usageRetryDelay {
		return cached.Usage
	}

	lockPath := cachePath + ".lock"
	lock, ok := acquireUsageLock(lockPath, now)
	if !ok {
		return cached.Usage
	}
	lock.Close()
	defer os.Remove(lockPath)
	cached.LastAttempt = now.Unix()
	_ = writeUsageCache(cachePath, cached)

	usage, err := fetchUsage()
	if err != nil {
		return cached.Usage
	}
	updated := usageCache{FetchedAt: time.Now().Unix(), LastAttempt: now.Unix(), Usage: usage}
	_ = writeUsageCache(cachePath, updated)
	return usage
}

func acquireUsageLock(path string, now time.Time) (*os.File, bool) {
	lock, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		return lock, true
	}
	info, statErr := os.Stat(path)
	if statErr != nil || now.Sub(info.ModTime()) <= usageLockMaxAge {
		return nil, false
	}
	if os.Remove(path) != nil {
		return nil, false
	}
	lock, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	return lock, err == nil
}

func readUsageCache(path string) (usageCache, bool) {
	var cache usageCache
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &cache) != nil {
		return cache, false
	}
	return cache, true
}

func writeUsageCache(path string, cache usageCache) error {
	data, err := json.Marshal(cache)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".codex-statusline-usage-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func fetchUsage() (Usage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "codex", "app-server", "--stdio")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return Usage{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Usage{}, err
	}
	if err := cmd.Start(); err != nil {
		return Usage{}, err
	}
	encoder := json.NewEncoder(stdin)
	requests := []any{
		map[string]any{"id": 1, "method": "initialize", "params": map[string]any{"clientInfo": map[string]string{"name": "codex-statusline", "version": "0.2.0"}, "capabilities": map[string]any{}}},
		map[string]any{"method": "initialized", "params": map[string]any{}},
		map[string]any{"id": 2, "method": "account/rateLimits/read", "params": map[string]any{}},
	}
	for _, request := range requests {
		if err := encoder.Encode(request); err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return Usage{}, err
		}
	}

	type response struct {
		ID     int `json:"id"`
		Result struct {
			RateLimits rateLimitSnapshot `json:"rateLimits"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		var message response
		if json.Unmarshal(scanner.Bytes(), &message) != nil || message.ID != 2 {
			continue
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if len(message.Error) > 0 && string(message.Error) != "null" {
			return Usage{}, fmt.Errorf("rate limit request failed")
		}
		return classifyUsage(message.Result.RateLimits), nil
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if err := scanner.Err(); err != nil {
		return Usage{}, err
	}
	return Usage{}, fmt.Errorf("rate limit response missing")
}

func classifyUsage(snapshot rateLimitSnapshot) Usage {
	var usage Usage
	for _, window := range []*rateLimitWindow{snapshot.Primary, snapshot.Secondary} {
		if window == nil || window.WindowDurationMins == nil {
			continue
		}
		value := window.UsedPercent
		if *window.WindowDurationMins == 10080 {
			resetsAt := int64(0)
			if window.ResetsAt != nil {
				resetsAt = *window.ResetsAt
			}
			usage.SevenDay = &UsageWindow{
				UsedPercent:        value,
				WindowDurationMins: *window.WindowDurationMins,
				ResetsAt:           resetsAt,
			}
		}
	}
	return usage
}

func darwinStats() Stats {
	cores := commandInt("sysctl", "-n", "hw.ncpu")
	if cores < 1 {
		cores = 1
	}
	load := commandFloatField("sysctl", []string{"-n", "vm.loadavg"}, 1)
	total := commandInt64("sysctl", "-n", "hw.memsize")
	pageSize := commandInt64("pagesize")
	active, wired := int64(0), int64(0)
	out, _ := exec.Command("vm_stat").Output()
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(strings.ReplaceAll(line, ".", ""))
		if len(fields) < 3 {
			continue
		}
		value, _ := strconv.ParseInt(fields[len(fields)-1], 10, 64)
		if strings.HasPrefix(line, "Pages active") {
			active = value
		}
		if strings.HasPrefix(line, "Pages wired") {
			wired = value
		}
	}
	mem := percent((active+wired)*pageSize, total)
	return Stats{CPU: int(load * 100 / float64(cores)), Mem: mem}
}

func linuxStats() Stats {
	cores := runtime.NumCPU()
	loadData, _ := os.ReadFile("/proc/loadavg")
	load, _ := strconv.ParseFloat(strings.Fields(string(loadData))[0], 64)
	total, available := int64(0), int64(0)
	file, err := os.Open("/proc/meminfo")
	if err == nil {
		defer file.Close()
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) < 2 {
				continue
			}
			value, _ := strconv.ParseInt(fields[1], 10, 64)
			switch strings.TrimSuffix(fields[0], ":") {
			case "MemTotal":
				total = value
			case "MemAvailable":
				available = value
			}
		}
	}
	return Stats{CPU: int(load * 100 / float64(cores)), Mem: percent(total-available, total)}
}

func commandInt(name string, args ...string) int {
	return int(commandInt64(name, args...))
}

func commandInt64(name string, args ...string) int64 {
	out, _ := exec.Command(name, args...).Output()
	value, _ := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	return value
}

func commandFloatField(name string, args []string, index int) float64 {
	out, _ := exec.Command(name, args...).Output()
	fields := strings.Fields(string(out))
	if index >= len(fields) {
		return 0
	}
	value, _ := strconv.ParseFloat(fields[index], 64)
	return value
}

func percent(part, total int64) int {
	if total <= 0 {
		return 0
	}
	return int(float64(part)*100/float64(total) + 0.5)
}
