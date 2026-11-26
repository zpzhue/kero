package terminal

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/chzyer/readline"
)

const mainPrompt = "你 > "

// Input reads conversation messages and responses to prompts.
type Input struct {
	rl         *readline.Instance
	reader     *bufio.Reader
	completing bool
}

// New opens terminal input with the given interactive history file.
func New(historyPath string) (*Input, error) {
	return NewWithReader(historyPath, nil)
}

// NewWithReader uses reader for plain input, or buffers stdin if reader is nil.
func NewWithReader(historyPath string, reader *bufio.Reader) (*Input, error) {
	if !interactiveTerminal() {
		if reader == nil {
			reader = bufio.NewReader(os.Stdin)
		}
		return &Input{reader: reader}, nil
	}
	input := &Input{}
	rl, err := readline.NewEx(&readline.Config{
		Prompt:                 mainPrompt,
		HistoryFile:            historyPath,
		AutoComplete:           slashCompleter{input: input},
		DisableAutoSaveHistory: true,
		InterruptPrompt:        "^C",
		EOFPrompt:              "退出",
	})
	if err != nil {
		return nil, err
	}
	input.rl = rl
	return input, nil
}

func interactiveTerminal() bool {
	return os.Getenv("TERM") != "dumb" &&
		readline.IsTerminal(int(os.Stdin.Fd())) &&
		readline.IsTerminal(int(os.Stdout.Fd()))
}

// Interactive reports whether input uses the readline editor.
func (i *Input) Interactive() bool {
	return i.rl != nil
}

func (i *Input) readLine() (string, error) {
	if i.Interactive() {
		return i.rl.Readline()
	}
	line, err := i.reader.ReadString('\n')
	if err == io.EOF && line != "" {
		return line, nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"), nil
}

// Read reads a complete message. A trailing backslash continues on the next line.
func (i *Input) Read() (string, error) {
	if i.Interactive() {
		defer i.rl.SetPrompt(mainPrompt)
	}
	var lines []string
	i.completing = true
	defer func() { i.completing = false }()
	for {
		line, err := i.readLine()
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
		i.completing = false
		if i.Interactive() {
			i.rl.SetPrompt("续行 > ")
		}
	}
	message := strings.Join(lines, "\n")
	if !i.Interactive() {
		return message, nil
	}
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
	prompt = Safe(prompt)
	if i.Interactive() {
		i.rl.SetPrompt(prompt)
		defer i.rl.SetPrompt(mainPrompt)
	} else {
		fmt.Fprintln(os.Stderr, prompt)
	}
	return i.readLine()
}

// Confirm asks for explicit approval; errors and other answers decline.
func (i *Input) Confirm(prompt string) bool {
	out := os.Stdout
	if !i.Interactive() {
		out = os.Stderr
	}
	fmt.Fprintln(out, "需要确认以下操作：")
	fmt.Fprintln(out, Safe(prompt))
	answer, err := i.Prompt("允许执行吗？[y/N] ")
	answer = strings.TrimSpace(answer)
	approved := err == nil && (strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes"))
	if approved {
		fmt.Fprintln(out, "已确认。")
	} else {
		fmt.Fprintln(out, "已取消此操作。")
	}
	return approved
}

// Close releases the terminal resources.
func (i *Input) Close() error {
	if !i.Interactive() {
		return nil
	}
	return i.rl.Close()
}

// PrintHelp prints available interactive commands and input instructions.
func PrintHelp() {
	fmt.Println("可用命令：")
	fmt.Println("  /help          显示帮助")
	fmt.Println("  /exit          退出 Kero")
	fmt.Println("  /sessions      选择当前工作区的会话")
	fmt.Println("  /resume <id>   恢复指定会话")
	fmt.Println("  /status        查看会话状态")
	fmt.Println("  /models        查看已配置的模型配置")
	fmt.Println("  /model <name>  为本次会话选择模型配置")
	fmt.Println("  /model auto    恢复自动或默认模型选择")
	if interactiveTerminal() {
		fmt.Println("输入消息后按回车发送；以 / 开头的输入会作为命令。")
		fmt.Println("输入 / 后按 Tab 补全命令；行末输入 \\ 可续行，方向键可浏览历史。")
	} else {
		fmt.Println("以 / 开头的输入会作为命令；行末 \\ 可继续输入下一行。")
	}
}
