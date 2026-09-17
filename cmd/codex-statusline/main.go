package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/c2keesey/codex-statusline/internal/config"
	"github.com/c2keesey/codex-statusline/internal/render"
)

const version = "0.3.12"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "install":
		err = install(os.Args[2:])
	case "render":
		err = renderCommand(os.Args[2:])
	case "doctor":
		err = doctor(os.Args[2:])
	case "preset":
		fmt.Println(config.StatusLine(config.DefaultItems))
	case "version", "--version", "-v":
		fmt.Println("codex-statusline " + version)
	case "help", "--help", "-h":
		usage()
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "codex-statusline:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`codex-statusline — adapt a Claude Code-style status line to Codex

Usage:
  codex-statusline install [--codex-config PATH] [--tmux] [--agent-deck]
  codex-statusline render [--cwd PATH] [--session NAME] [--tmux-style]
  codex-statusline doctor [--codex-config PATH]
  codex-statusline preset
  codex-statusline version
`)
}

func install(args []string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet("install", flag.ContinueOnError)
	codexPath := flags.String("codex-config", filepath.Join(home, ".codex", "config.toml"), "Codex config path")
	tmuxPath := flags.String("tmux-config", filepath.Join(home, ".tmux.conf"), "tmux config path")
	withTmux := flags.Bool("tmux", false, "install the optional tmux companion")
	agentDeckPath := flags.String("agent-deck-config", filepath.Join(home, ".config", "agent-deck", "config.toml"), "Agent Deck config path")
	withAgentDeck := flags.Bool("agent-deck", false, "install the companion in Agent Deck's isolated tmux")
	if err := flags.Parse(args); err != nil {
		return err
	}
	changed, backup, err := config.InstallCodex(*codexPath, config.DefaultItems)
	if err != nil {
		return err
	}
	printInstallResult("Codex config", *codexPath, changed, backup)
	if *withTmux {
		changed, backup, err = config.InstallTmux(*tmuxPath)
		if err != nil {
			return err
		}
		printInstallResult("tmux config", *tmuxPath, changed, backup)
		if exec.Command("tmux", "source-file", *tmuxPath).Run() == nil {
			fmt.Println("Reloaded tmux config")
		}
	}
	if *withAgentDeck {
		changed, backup, err = config.InstallAgentDeck(*agentDeckPath)
		if err != nil {
			return err
		}
		printInstallResult("Agent Deck config", *agentDeckPath, changed, backup)
		fmt.Println("Restart Agent Deck sessions to apply the persistent override")
	}
	return nil
}

func printInstallResult(label, path string, changed bool, backup string) {
	if !changed {
		fmt.Printf("%s already configured: %s\n", label, path)
		return
	}
	fmt.Printf("Configured %s: %s\n", label, path)
	if backup != "" {
		fmt.Printf("Backup: %s\n", backup)
	}
}

func renderCommand(args []string) error {
	flags := flag.NewFlagSet("render", flag.ContinueOnError)
	cwd := flags.String("cwd", "", "active pane directory")
	session := flags.String("session", "", "Agent Deck tmux session name")
	tmuxStyle := flags.Bool("tmux-style", false, "use Claude-style tmux colors")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *cwd == "" {
		*cwd, _ = os.Getwd()
	}
	if *tmuxStyle {
		fmt.Println(render.LineWithSessionTMUX(*cwd, *session))
	} else {
		fmt.Println(render.LineWithSession(*cwd, *session))
	}
	return nil
}

func doctor(args []string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	codexPath := flags.String("codex-config", filepath.Join(home, ".codex", "config.toml"), "Codex config path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	failed := false
	if out, err := exec.Command("codex", "--version").Output(); err == nil {
		fmt.Printf("✓ %s", out)
	} else {
		fmt.Println("✗ codex is not available")
		failed = true
	}
	data, err := os.ReadFile(*codexPath)
	if err != nil {
		fmt.Printf("✗ cannot read %s: %v\n", *codexPath, err)
		failed = true
	} else if strings.Contains(string(data), config.StatusLine(config.DefaultItems)) {
		fmt.Println("✓ Claude-style native preset is installed")
	} else {
		fmt.Println("✗ native preset is not installed; run codex-statusline install")
		failed = true
	}
	if _, err := exec.LookPath("tmux"); err == nil {
		fmt.Println("✓ tmux is available (optional companion supported)")
	} else {
		fmt.Println("· tmux is unavailable (native Codex status line still works)")
	}
	if failed {
		return fmt.Errorf("doctor found problems")
	}
	return nil
}
