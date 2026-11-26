package terminal

import (
	"fmt"
	"strings"

	"github.com/chzyer/readline"
)

const mainPrompt = "> "

// Input handles interactive messages and prompts without adding responses to history.
type Input struct {
	rl *readline.Instance
}

// New opens an interactive input with the given history file.
func New(historyPath string) (*Input, error) {
	rl, err := readline.NewEx(&readline.Config{
		Prompt:                 mainPrompt,
		HistoryFile:            historyPath,
		DisableAutoSaveHistory: true,
		InterruptPrompt:        "^C",
		EOFPrompt:              "exit",
	})
	if err != nil {
		return nil, err
	}
	return &Input{rl: rl}, nil
}

// Read reads a complete message. A trailing backslash continues on the next line.
func (i *Input) Read() (string, error) {
	defer i.rl.SetPrompt(mainPrompt)
	var lines []string
	for {
		line, err := i.rl.Readline()
		if err != nil {
			return "", err
		}
		more := strings.HasSuffix(line, "\\")
		if more {
			line = strings.TrimSuffix(line, "\\")
		}
		lines = append(lines, line)
		if !more {
			break
		}
		i.rl.SetPrompt("... ")
	}
	message := strings.Join(lines, "\n")
	trimmed := strings.TrimSpace(message)
	if trimmed != "" && !strings.HasPrefix(trimmed, "/") {
		if err := i.rl.SaveHistory(message); err != nil {
			return "", err
		}
	}
	return message, nil
}

// Prompt reads a response without saving it to message history.
func (i *Input) Prompt(prompt string) (string, error) {
	i.rl.SetPrompt(prompt)
	defer i.rl.SetPrompt(mainPrompt)
	return i.rl.Readline()
}

// Confirm asks for explicit approval; errors and all answers other than y decline.
func (i *Input) Confirm(prompt string) bool {
	fmt.Println(prompt)
	answer, err := i.Prompt("Allow? [y/N] ")
	return err == nil && strings.EqualFold(strings.TrimSpace(answer), "y")
}

// Close releases the terminal resources.
func (i *Input) Close() error {
	return i.rl.Close()
}

// PrintHelp prints available interactive commands and input instructions.
func PrintHelp() {
	fmt.Println("Commands:")
	fmt.Println("  /help          Show this help")
	fmt.Println("  /exit          Exit Kero")
	fmt.Println("  /sessions      Select a session")
	fmt.Println("  /resume <id>   Resume a session")
	fmt.Println("  /status        Show session status")
	fmt.Println("  /models        List configured model profiles")
	fmt.Println("  /model <name>  Select a model profile for this session")
	fmt.Println("  /model auto    Use configured automatic/default routing")
	fmt.Println("Lines beginning with / are interpreted as commands.")
	fmt.Println("End a line with \\ to continue the message on the next line.")
}
