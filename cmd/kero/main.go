package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/chzyer/readline"
	"kero/internal/config"
	"kero/internal/provider"
	"kero/internal/session"
	"kero/internal/tools"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "kero:", err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) > 1 && os.Args[1] == "config" {
		if len(os.Args) > 2 && os.Args[2] == "path" {
			p, e := config.Path()
			if e == nil {
				fmt.Println(p)
			}
			return e
		}
		c, e := config.Load()
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			return e
		}
		_, e = config.Configure(bufio.NewReader(os.Stdin), c)
		return e
	}
	fs := flag.NewFlagSet("kero", flag.ContinueOnError)
	resume := fs.String("resume", "", "resume session ID")
	version := fs.Bool("version", false, "show version")
	if e := fs.Parse(os.Args[1:]); e != nil {
		return e
	}
	if *version {
		fmt.Println("kero dev")
		return nil
	}
	c, e := config.Load()
	if errors.Is(e, os.ErrNotExist) {
		fmt.Println("First-time setup:")
		c, e = config.Configure(bufio.NewReader(os.Stdin), config.Config{})
	}
	if e != nil {
		return e
	}
	root, e := os.Getwd()
	if e != nil {
		return e
	}
	root, e = filepath.EvalSymlinks(root)
	if e != nil {
		return e
	}
	s, messages, e := session.Open(*resume, root)
	if e != nil {
		return e
	}
	defer s.Close()
	dir, e := os.UserConfigDir()
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Join(dir, "kero"), 0700); e != nil {
		return e
	}
	rl, e := readline.NewEx(&readline.Config{Prompt: "> ", HistoryFile: filepath.Join(dir, "kero", "history"), DisableAutoSaveHistory: true, InterruptPrompt: "^C", EOFPrompt: "exit"})
	if e != nil {
		return e
	}
	defer rl.Close()
	fmt.Printf("Kero session %s | %s | %s\nEnter submits; end a line with \\ for multiline. /exit to quit.\n", s.ID(), root, c.Model)
	client := provider.Client{BaseURL: c.BaseURL, Model: c.Model}
	if c.APIKeyEnv != "" {
		client.APIKey = os.Getenv(c.APIKeyEnv)
		if client.APIKey == "" {
			return fmt.Errorf("API key environment variable %s is empty", c.APIKeyEnv)
		}
	}
	for {
		text, err := readInput(rl)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			if errors.Is(err, readline.ErrInterrupt) {
				continue
			}
			return err
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		if text == "/exit" {
			return nil
		}
		if e = rl.SaveHistory(text); e != nil {
			return e
		}
		msg := provider.Message{Role: "user", Content: text}
		if e = s.AppendMessage(msg); e != nil {
			return e
		}
		messages = append(messages, msg)
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		messages, e = turn(ctx, rl, s, &client, root, c.Permission, messages)
		stop()
		if e != nil {
			fmt.Fprintln(os.Stderr, "\n", e)
			if er := s.AppendEvent("error", map[string]string{"message": e.Error()}); er != nil {
				return er
			}
		}
	}
}
func readInput(rl *readline.Instance) (string, error) {
	var lines []string
	for {
		line, e := rl.Readline()
		if e != nil {
			return "", e
		}
		more := strings.HasSuffix(line, "\\")
		if more {
			line = strings.TrimSuffix(line, "\\")
		}
		lines = append(lines, line)
		if !more {
			rl.SetPrompt("> ")
			return strings.Join(lines, "\n"), nil
		}
		rl.SetPrompt("... ")
	}
}
func turn(ctx context.Context, rl *readline.Instance, s *session.Session, client *provider.Client, root, mode string, messages []provider.Message) ([]provider.Message, error) {
	for range 32 {
		request := append([]provider.Message{{Role: "system", Content: "You are Kero, a coding assistant. Inspect relevant files before changing them, use the available tools, and report results accurately. Work in the current workspace."}}, messages...)
		m, e := client.Chat(ctx, request, tools.Definitions(), func(t string) { fmt.Print(t) })
		fmt.Println()
		if e != nil {
			return messages, e
		}
		if e = s.AppendMessage(m); e != nil {
			return messages, e
		}
		messages = append(messages, m)
		if len(m.ToolCalls) == 0 {
			return messages, nil
		}
		for _, call := range m.ToolCalls {
			if ctx.Err() != nil {
				t := provider.Message{Role: "tool", ToolCallID: call.ID, Content: "Cancelled before execution"}
				if e = s.AppendMessage(t); e != nil {
					return messages, e
				}
				messages = append(messages, t)
				continue
			}
			confirm := func(prompt string) bool {
				fmt.Println(prompt)
				rl.SetPrompt("Allow? [y/N] ")
				answer, err := rl.Readline()
				rl.SetPrompt("> ")
				return err == nil && strings.EqualFold(strings.TrimSpace(answer), "y")
			}
			result := tools.Execute(ctx, root, mode, call, confirm)
			t := provider.Message{Role: "tool", ToolCallID: call.ID, Content: result}
			if e = s.AppendMessage(t); e != nil {
				return messages, e
			}
			messages = append(messages, t)
			fmt.Printf("[%s] %s\n", call.Function.Name, result)
		}
	}
	return messages, errors.New("tool call limit reached")
}
