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
	"strconv"
	"strings"
	"time"
)

var agentDeckSessionPattern = regexp.MustCompile(`^agentdeck_(.+)_([[:xdigit:]]{8})$`)
var nativeContextPattern = regexp.MustCompile(`\bContext ([0-9]+)% used\b`)
var claudeSessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
var codexSessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func Line(cwd string) string {
	return LineWithSession(cwd, "")
}

func LineWithSession(cwd, session string) string {
	groups := make([]string, 0, 4)
	if value, ok := ContextPercent(session); ok {
		groups = append(groups, fmt.Sprintf("%d%%", value))
	}
	if label := CompactSession(session); label != "" {
		groups = append(groups, label)
	}
	return strings.Join(groups, " │ ")
}

const (
	tmuxReset        = "#[default]"
	tmuxGray         = "#[fg=default,dim]"
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
	groups := make([]string, 0, 4)
	if value, ok := contextPercentForSession(session, metadata.instanceID, metadata.codexSessionID); ok {
		groups = append(groups, tmuxPercentColor(value)+fmt.Sprintf("%d%%", value)+tmuxReset)
	}
	if label := CompactSession(session); label != "" {
		groups = append(groups, tmuxSummaryColor+label+tmuxReset)
	}
	return tmuxTheme(strings.Join(groups, " "+tmuxGray+"│"+tmuxReset+" "), metadata.theme)
}

const (
	ansiReset        = "\x1b[0m"
	ansiGray         = "\x1b[90m"
	ansiSummaryColor = "\x1b[94m"
)

// LineForClaudeANSI renders the same footer content as the Agent Deck tmux
// companion, using terminal escapes that Claude Code's native statusline can
// display. Claude sessions own this row natively, so their tmux footer is
// suppressed by LineWithSessionTMUX.
func LineForClaudeANSI(claudeSessionID, session string) string {
	state, ok := loadClaudeState(claudeSessionID)
	if !ok {
		return ""
	}
	groups := make([]string, 0, 4)
	groups = append(groups, ansiPercentColor(state.ContextPercent)+fmt.Sprintf("%d%%", state.ContextPercent)+ansiReset)
	if label := CompactSession(session); label != "" {
		groups = append(groups, ansiSummaryColor+label+ansiReset)
	}
	return strings.Join(groups, " "+ansiGray+"│"+ansiReset+" ")
}

func ansiPercentColor(percent int) string {
	colour := 10
	switch {
	case percent >= 90:
		colour = 9
	case percent >= 80:
		colour = 166
	case percent >= 60:
		colour = 136
	}
	return fmt.Sprintf("\x1b[1;38;5;%dm", colour)
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
	instanceID      string
	codex           bool
	codexSessionID  string
	claudeSessionID string
	theme           string
}

type claudeState struct {
	FetchedAt      int64 `json:"fetched_at"`
	ContextPercent int   `json:"context_percent"`
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
	colorfgbg := ""
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
			metadata.codexSessionID = strings.TrimSpace(value)
			metadata.codex = metadata.codexSessionID != ""
		case "CLAUDE_SESSION_ID":
			metadata.claudeSessionID = strings.TrimSpace(value)
		case "COLORFGBG":
			colorfgbg = strings.TrimSpace(value)
		}
	}
	if metadata.theme == "" && lightColorFGBG(colorfgbg) {
		metadata.theme = "seaglass"
	}
	return metadata
}

// lightColorFGBG reports whether a COLORFGBG value ("fg;bg", background last)
// names a light background. Agent Deck keeps the session's COLORFGBG on the
// viewer's theme, so the footer follows it without a restart.
func lightColorFGBG(value string) bool {
	idx := strings.LastIndex(value, ";")
	if idx < 0 {
		return false
	}
	bg, err := strconv.Atoi(value[idx+1:])
	return err == nil && bg >= 8
}

func claudeStatePath(sessionID string) string {
	return filepath.Join(os.TempDir(), "claude-statusline-"+sessionID+".json")
}

func loadClaudeState(sessionID string) (claudeState, bool) {
	if !claudeSessionIDPattern.MatchString(sessionID) {
		return claudeState{}, false
	}
	data, err := os.ReadFile(claudeStatePath(sessionID))
	if err != nil {
		return claudeState{}, false
	}
	var state claudeState
	if json.Unmarshal(data, &state) != nil || state.ContextPercent < 0 {
		return claudeState{}, false
	}
	return state, true
}

// ContextPercent reads the latest context count from the Codex rollout that
// belongs to an Agent Deck session. Codex reports cumulative thread usage as
// well, but the last request size is the value currently occupying context.
func ContextPercent(session string) (int, bool) {
	metadata, ok := agentDeckSessionMetadata(session)
	if !ok || !metadata.codex {
		return 0, false
	}
	return contextPercentForSession(session, metadata.instanceID, metadata.codexSessionID)
}

func contextPercentForSession(session, instanceID, codexSessionID string) (int, bool) {
	if value, ok := contextPercentForInstance(instanceID); ok {
		return value, true
	}
	if value, ok := contextPercentForCodexSession(codexSessionID); ok {
		return value, true
	}
	return nativeContextPercent(session)
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

func contextPercentForCodexSession(sessionID string) (int, bool) {
	if !codexSessionIDPattern.MatchString(sessionID) {
		return 0, false
	}
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return 0, false
		}
		codexHome = filepath.Join(home, ".codex")
	}
	matches, err := filepath.Glob(filepath.Join(codexHome, "sessions", "*", "*", "*", "*"+sessionID+"*.jsonl"))
	if err != nil || len(matches) == 0 {
		return 0, false
	}
	return contextPercentFromRollout(matches[len(matches)-1])
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
		value = percent(used, effectiveWindow)
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

func percent(part, total int64) int {
	if total <= 0 {
		return 0
	}
	return int(float64(part)*100/float64(total) + 0.5)
}
