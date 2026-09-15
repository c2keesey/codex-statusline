package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var DefaultItems = []string{
	"git-branch",
	"branch-changes",
	"current-dir",
	"thread-title",
	"context-used",
	"weekly-limit",
	"model-with-reasoning",
}

var sectionPattern = regexp.MustCompile(`^\s*\[[^[]`)
var statusLinePattern = regexp.MustCompile(`^\s*status_line\s*=`)

func StatusLine(items []string) string {
	quoted := make([]string, len(items))
	for i, item := range items {
		quoted[i] = fmt.Sprintf("%q", item)
	}
	return "status_line = [" + strings.Join(quoted, ", ") + "]"
}

// ApplyStatusLine changes only tui.status_line and preserves the rest of the
// user's config byte-for-byte, apart from normalizing the final newline.
func ApplyStatusLine(input string, items []string) (string, bool) {
	wanted := StatusLine(items)
	lines := strings.Split(strings.TrimSuffix(input, "\n"), "\n")

	tuiStart, tuiEnd := -1, len(lines)
	for i, line := range lines {
		if strings.TrimSpace(line) == "[tui]" {
			tuiStart = i
			continue
		}
		if tuiStart >= 0 && sectionPattern.MatchString(line) {
			tuiEnd = i
			break
		}
	}

	if tuiStart < 0 {
		if len(lines) == 1 && lines[0] == "" {
			lines = nil
		}
		if len(lines) > 0 && lines[len(lines)-1] != "" {
			lines = append(lines, "")
		}
		lines = append(lines, "[tui]", "  "+wanted)
		return strings.Join(lines, "\n") + "\n", true
	}

	for i := tuiStart + 1; i < tuiEnd; i++ {
		if !statusLinePattern.MatchString(lines[i]) {
			continue
		}
		indent := lines[i][:len(lines[i])-len(strings.TrimLeft(lines[i], " \t"))]
		end := i
		balance := strings.Count(lines[i], "[") - strings.Count(lines[i], "]")
		for balance > 0 && end+1 < tuiEnd {
			end++
			balance += strings.Count(lines[end], "[") - strings.Count(lines[end], "]")
		}
		replacement := indent + wanted
		if end == i && lines[i] == replacement {
			return input, false
		}
		lines = append(lines[:i], append([]string{replacement}, lines[end+1:]...)...)
		return strings.Join(lines, "\n") + "\n", true
	}

	lines = append(lines[:tuiEnd], append([]string{"  " + wanted}, lines[tuiEnd:]...)...)
	return strings.Join(lines, "\n") + "\n", true
}

func InstallCodex(path string, items []string) (bool, string, error) {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, "", err
	}
	updated, changed := ApplyStatusLine(string(data), items)
	if !changed {
		return false, "", nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, "", err
	}
	backup := ""
	mode := os.FileMode(0o600)
	if info, statErr := os.Stat(path); statErr == nil {
		mode = info.Mode().Perm()
	}
	if len(data) > 0 {
		backup = path + ".codex-statusline.bak"
		if _, err := os.Stat(backup); os.IsNotExist(err) {
			if err := os.WriteFile(backup, data, mode); err != nil {
				return false, "", err
			}
		}
	}
	return true, backup, atomicWrite(path, []byte(updated), mode)
}

const tmuxStart = "# codex-statusline:start"
const tmuxEnd = "# codex-statusline:end"

const TmuxBlock = `# codex-statusline:start
set -g status-interval 2
set -g status-right '#(codex-statusline render --cwd "#{pane_current_path}") #[fg=colour240]│#[default] %H:%M'
# codex-statusline:end`

const AgentDeckStatusLeft = `  #(~/.local/bin/codex-statusline render --tmux-style --cwd "#{pane_current_path}" --session "#{session_name}") `

const AgentDeckStatusLeftLength = "200"

const AgentDeckStatusStyle = "bg=default,fg=default"

func ApplyTmux(input string) (string, bool) {
	trimmed := strings.TrimSuffix(input, "\n")
	start := strings.Index(trimmed, tmuxStart)
	if start >= 0 {
		endRel := strings.Index(trimmed[start:], tmuxEnd)
		if endRel >= 0 {
			end := start + endRel + len(tmuxEnd)
			updated := trimmed[:start] + TmuxBlock + trimmed[end:]
			updated = strings.TrimSuffix(updated, "\n") + "\n"
			return updated, updated != input
		}
	}
	if trimmed != "" {
		trimmed += "\n\n"
	}
	return trimmed + TmuxBlock + "\n", true
}

func InstallTmux(path string) (bool, string, error) {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, "", err
	}
	updated, changed := ApplyTmux(string(data))
	if !changed {
		return false, "", nil
	}
	backup := ""
	mode := os.FileMode(0o600)
	if info, statErr := os.Stat(path); statErr == nil {
		mode = info.Mode().Perm()
	}
	if len(data) > 0 {
		backup = path + ".codex-statusline.bak"
		if _, err := os.Stat(backup); os.IsNotExist(err) {
			if err := os.WriteFile(backup, data, mode); err != nil {
				return false, "", err
			}
		}
	}
	return true, backup, atomicWrite(path, []byte(updated), mode)
}

// ApplyAgentDeck installs a status-right override into [tmux.options]. Agent
// Deck uses an isolated tmux server and a per-session status bar, so ~/.tmux.conf
// alone cannot customize sessions launched by Agent Deck.
func ApplyAgentDeck(input string) (string, bool) {
	wanted := []struct {
		key  string
		line string
	}{
		{"status-left", "    status-left = '" + AgentDeckStatusLeft + "'"},
		{"status-left-length", "    status-left-length = \"" + AgentDeckStatusLeftLength + "\""},
		{"status-style", "    status-style = \"" + AgentDeckStatusStyle + "\""},
		{"window-status-format", `    window-status-format = ""`},
		{"window-status-current-format", `    window-status-current-format = ""`},
		{"status-right", `    status-right = ""`},
		{"status-right-length", `    status-right-length = "0"`},
	}
	lines := strings.Split(strings.TrimSuffix(input, "\n"), "\n")
	sectionStart, sectionEnd := -1, len(lines)
	for i, candidate := range lines {
		if strings.TrimSpace(candidate) == "[tmux.options]" {
			sectionStart = i
			continue
		}
		if sectionStart >= 0 && sectionPattern.MatchString(candidate) {
			sectionEnd = i
			break
		}
	}
	if sectionStart < 0 {
		if len(lines) > 0 && lines[len(lines)-1] != "" {
			lines = append(lines, "")
		}
		lines = append(lines, "[tmux.options]")
		for _, option := range wanted {
			lines = append(lines, option.line)
		}
		return strings.Join(lines, "\n") + "\n", true
	}
	found := make(map[string]bool, len(wanted))
	for i := sectionStart + 1; i < sectionEnd; i++ {
		trimmed := strings.TrimSpace(lines[i])
		parts := strings.SplitN(trimmed, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		for _, option := range wanted {
			if key == option.key {
				lines[i] = option.line
				found[key] = true
				break
			}
		}
	}
	missing := make([]string, 0, len(wanted))
	for _, option := range wanted {
		if !found[option.key] {
			missing = append(missing, option.line)
		}
	}
	if len(missing) > 0 {
		lines = append(lines[:sectionEnd], append(missing, lines[sectionEnd:]...)...)
	}
	updated := strings.Join(lines, "\n") + "\n"
	return updated, updated != input
}

func InstallAgentDeck(path string) (bool, string, error) {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, "", err
	}
	updated, changed := ApplyAgentDeck(string(data))
	if !changed {
		return false, "", nil
	}
	mode := os.FileMode(0o600)
	if info, statErr := os.Stat(path); statErr == nil {
		mode = info.Mode().Perm()
	}
	backup := ""
	if len(data) > 0 {
		backup = path + ".codex-statusline.bak"
		if _, err := os.Stat(backup); os.IsNotExist(err) {
			if err := os.WriteFile(backup, data, mode); err != nil {
				return false, "", err
			}
		}
	}
	return true, backup, atomicWrite(path, []byte(updated), mode)
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".codex-statusline-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
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
