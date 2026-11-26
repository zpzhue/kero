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
	"kero/internal/contextbudget"
	"kero/internal/provider"
	"kero/internal/session"
	"kero/internal/terminal"
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
	defer func() { _ = s.Close() }()
	dir, e := os.UserConfigDir()
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Join(dir, "kero"), 0700); e != nil {
		return e
	}
	input, e := terminal.New(filepath.Join(dir, "kero", "history"))
	if e != nil {
		return e
	}
	defer input.Close()
	fmt.Printf("Kero session %s | %s | %s\nType /help for commands.\n", s.ID(), root, c.Model)
	manualProfile := ""
	activeModel := c.Model
	workspace := new(tools.Workspace)
	for {
		text, err := input.Read()
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
		command := strings.TrimSpace(text)
		if command == "/exit" {
			return nil
		}
		if command == "/help" {
			terminal.PrintHelp()
			continue
		}
		if command == "/status" {
			fmt.Printf("Session: %s\nWorkspace: %s\nModel: %s\nPermissions: %s\n", s.ID(), root, activeModel, c.Permission)
			continue
		}
		if command == "/models" {
			for _, name := range c.ProfileNames() {
				fmt.Println(" ", name)
			}
			continue
		}
		if strings.HasPrefix(command, "/model ") {
			name := strings.TrimSpace(strings.TrimPrefix(command, "/model "))
			if name == "auto" {
				manualProfile = ""
				fmt.Println("Automatic/default model selection enabled.")
				continue
			}
			profile, selectErr := c.SelectProfile(name)
			if selectErr == nil {
				_, selectErr = profileClient(profile)
			}
			if selectErr != nil {
				fmt.Fprintln(os.Stderr, selectErr)
				continue
			}
			manualProfile = name
			activeModel = profile.Model
			fmt.Printf("Selected profile %s (%s).\n", name, profile.Model)
			continue
		}
		if command == "/sessions" || strings.HasPrefix(command, "/resume ") {
			var id string
			if command == "/sessions" {
				id, err = chooseSession(input, root)
			} else {
				id = strings.TrimSpace(strings.TrimPrefix(command, "/resume "))
			}
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				continue
			}
			if id == "" {
				continue
			}
			if id == s.ID() {
				fmt.Println("Already in this session.")
				continue
			}
			next, history, openErr := session.Open(id, root)
			if openErr != nil {
				fmt.Fprintln(os.Stderr, openErr)
				continue
			}
			if closeErr := s.Close(); closeErr != nil {
				_ = next.Close()
				return closeErr
			}
			s, messages = next, history
			workspace = new(tools.Workspace)
			fmt.Printf("Resumed session %s (%d messages).\n", s.ID(), len(messages))
			for _, m := range messages {
				if (m.Role == "user" || m.Role == "assistant") && m.Content != "" {
					fmt.Printf("[%s] %s\n", m.Role, m.Content)
				}
			}
			continue
		}
		if strings.HasPrefix(command, "/") {
			fmt.Println("Unknown command. Type /help for commands.")
			continue
		}
		decision, routeErr := c.RouteProfile(text, manualProfile)
		if routeErr != nil {
			fmt.Fprintln(os.Stderr, routeErr)
			continue
		}
		client, routeErr := profileClient(decision.Profile)
		if routeErr != nil {
			fmt.Fprintln(os.Stderr, routeErr)
			continue
		}
		activeModel = decision.Profile.Model
		fmt.Printf("[model] %s (%s): %s\n", decision.Name, activeModel, decision.Reason)
		if e = s.AppendEvent("model_selection", map[string]string{"profile": decision.Name, "model": activeModel, "reason": decision.Reason}); e != nil {
			return e
		}
		msg := provider.Message{Role: "user", Content: text}
		if e = s.AppendMessage(msg); e != nil {
			return e
		}
		messages = append(messages, msg)
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		messages, e = turn(ctx, input, s, &client, workspace, root, c.Permission, decision.Profile.ContextTokens, messages)
		stop()
		if e != nil {
			fmt.Fprintln(os.Stderr, "\n", e)
			if er := s.AppendEvent("error", map[string]string{"message": e.Error()}); er != nil {
				return er
			}
		}
	}
}
func turn(ctx context.Context, input *terminal.Input, s *session.Session, client *provider.Client, workspace *tools.Workspace, root, mode string, window int, messages []provider.Message) ([]provider.Message, error) {
	for range 32 {
		fmt.Println("[assistant] Thinking…")
		request := append([]provider.Message{{Role: "system", Content: "You are Kero, a coding assistant. Inspect relevant files before changing them, use the available tools, and report results accurately. Work in the current workspace."}}, messages...)
		if window <= 0 {
			window = 32768
		}
		definitions := tools.Definitions()
		prepared, stats, budgetErr := contextbudget.Prepare(request, definitions, contextbudget.Limits{MaxWindow: window, ReserveOutput: window / 8})
		if budgetErr != nil {
			return messages, budgetErr
		}
		if stats.DroppedMessages > 0 {
			fmt.Printf("[context] retained recent messages; omitted %d older messages for this request\n", stats.DroppedMessages)
		}
		m, e := client.Chat(ctx, prepared, definitions, func(t string) { fmt.Print(t) })
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
		if len(m.ToolCalls) > 0 {
			fmt.Printf("[assistant] %d tool call(s)\n", len(m.ToolCalls))
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
			confirm := input.Confirm
			fmt.Printf("[tool] Running %s…\n", call.Function.Name)
			result := workspace.Execute(ctx, root, mode, call, confirm)
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

func profileClient(profile config.Profile) (provider.Client, error) {
	c := provider.Client{BaseURL: profile.BaseURL, Model: profile.Model}
	if profile.APIKeyEnv != "" {
		c.APIKey = os.Getenv(profile.APIKeyEnv)
		if c.APIKey == "" {
			return c, fmt.Errorf("API key environment variable %q is not set; set it, or run 'kero config' and enter - to clear it for an unauthenticated endpoint", profile.APIKeyEnv)
		}
	}
	return c, nil
}
