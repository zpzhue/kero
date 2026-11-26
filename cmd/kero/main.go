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
		_, e = config.ConfigureMenu(bufio.NewReader(os.Stdin), c)
		if errors.Is(e, config.ErrCancelled) {
			fmt.Println("已取消配置，未保存更改。")
			return nil
		}
		return e
	}
	fs := flag.NewFlagSet("kero", flag.ContinueOnError)
	resume := fs.String("resume", "", "恢复指定 ID 的会话")
	version := fs.Bool("version", false, "显示版本")
	if e := fs.Parse(os.Args[1:]); e != nil {
		return e
	}
	if *version {
		fmt.Println("kero dev")
		return nil
	}
	stdin := bufio.NewReader(os.Stdin)
	c, e := config.Load()
	if errors.Is(e, os.ErrNotExist) {
		fmt.Println("欢迎使用 Kero，请先完成初始配置。")
		c, e = config.Configure(stdin, config.Config{})
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
	input, e := terminal.NewWithReader(filepath.Join(dir, "kero", "history"), stdin)
	if e != nil {
		return e
	}
	defer input.Close()
	fmt.Printf("Kero\n工作区  %s\n会话    %s\n默认模型  %s    权限  %s\n", terminal.Safe(root), s.ID(), terminal.Safe(c.Model), c.Permission)
	if input.Interactive() {
		fmt.Println("Enter 发送 · 行末 \\ 续行 · /help 查看命令")
	}
	fmt.Println()
	if *resume != "" {
		printRecoveredConversation(messages)
	}
	manualProfile := ""
	var lastRequest, lastReason string
	workspace := new(tools.Workspace)
	for {
		text, err := input.Read()
		if errors.Is(err, io.EOF) {
			fmt.Println("再见。")
			return nil
		}
		if err != nil {
			if errors.Is(err, readline.ErrInterrupt) {
				fmt.Println("已取消输入。")
				continue
			}
			return err
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		command := strings.TrimSpace(text)
		if command == "/exit" {
			fmt.Println("再见。")
			return nil
		}
		if command == "/help" {
			terminal.PrintHelp()
			continue
		}
		if command == "/status" {
			fmt.Printf("会话  %s\n工作区  %s\n权限  %s\n默认模型  %s\n", s.ID(), terminal.Safe(root), c.Permission, terminal.Safe(c.Model))
			if manualProfile != "" {
				fmt.Printf("下次选择  手动 · %s\n", terminal.Safe(manualProfile))
			} else if c.Routing.Enabled {
				fmt.Println("下次选择  自动路由")
			} else {
				fmt.Println("下次选择  默认方案")
			}
			if lastRequest == "" {
				fmt.Println("本次打开后尚未请求模型")
			} else {
				fmt.Printf("本次打开后最近请求  %s\n选择原因  %s\n", terminal.Safe(lastRequest), terminal.Safe(lastReason))
			}
			continue
		}
		if command == "/models" {
			fmt.Println("已配置的模型：")
			for _, name := range c.ProfileNames() {
				fmt.Println(" ", name)
			}
			continue
		}
		if strings.HasPrefix(command, "/model ") {
			name := strings.TrimSpace(strings.TrimPrefix(command, "/model "))
			if name == "auto" {
				manualProfile = ""
				fmt.Println("已恢复自动或默认模型选择。")
				continue
			}
			profile, selectErr := c.SelectProfile(name)
			if selectErr == nil {
				_, selectErr = profileClient(profile)
			}
			if selectErr != nil {
				fmt.Fprintln(os.Stderr, "选择模型失败：", selectErr)
				continue
			}
			manualProfile = name
			fmt.Printf("已选择模型配置 %s（%s）。\n", name, profile.Model)
			continue
		}
		if command == "/sessions" || strings.HasPrefix(command, "/resume ") {
			var id string
			if command == "/sessions" {
				id, err = chooseSession(input, root, s.ID())
			} else {
				id = strings.TrimSpace(strings.TrimPrefix(command, "/resume "))
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					fmt.Println("再见。")
					return nil
				}
				if errors.Is(err, readline.ErrInterrupt) {
					fmt.Println("已取消选择会话。")
					continue
				}
				fmt.Fprintln(os.Stderr, err)
				continue
			}
			if id == "" {
				continue
			}
			if id == s.ID() {
				fmt.Println("当前已在此会话中。")
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
			lastRequest, lastReason = "", ""
			fmt.Printf("已恢复会话 %s\n", s.ID())
			printRecoveredConversation(messages)
			continue
		}
		if strings.HasPrefix(command, "/") {
			fmt.Println("未知命令，输入 /help 查看可用命令。")
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
		fmt.Printf("\n模型  %s（%s）\n", terminal.Safe(decision.Name), terminal.Safe(decision.Profile.Model))
		if e = s.AppendEvent("model_selection", map[string]string{"profile": decision.Name, "model": decision.Profile.Model, "reason": decision.Reason}); e != nil {
			return e
		}
		msg := provider.Message{Role: "user", Content: text}
		if e = s.AppendMessage(msg); e != nil {
			return e
		}
		messages = append(messages, msg)
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		messages, e = turn(ctx, input, s, &client, workspace, root, c.Permission, decision.Profile.ContextTokens, messages, func() {
			lastRequest = decision.Name + " (" + decision.Profile.Model + ")"
			lastReason = decision.Reason
		})
		stop()
		if e != nil {
			if errors.Is(e, context.Canceled) {
				fmt.Fprintln(os.Stderr, "本次回复已取消。")
			} else {
				fmt.Fprintln(os.Stderr, "回复出错：", e)
			}
			if er := s.AppendEvent("error", map[string]string{"message": e.Error()}); er != nil {
				return er
			}
		}
	}
}
func turn(ctx context.Context, input *terminal.Input, s *session.Session, client *provider.Client, workspace *tools.Workspace, root, mode string, window int, messages []provider.Message, onRequest func()) ([]provider.Message, error) {
	var trace commandTrace
	defer trace.print(os.Stdout)
	for range 32 {
		request := append([]provider.Message{{Role: "system", Content: "You are Kero, a coding assistant. Inspect relevant files before changing them, use the available tools, and work in the current workspace. Report only actions and verification actually performed in this conversation, based on tool calls and their results. Never claim that a command, test, formatter, or check ran unless you ran it and observed its result. Distinguish requests denied or cancelled from executed commands, and state when verification was not performed."}}, messages...)
		if window <= 0 {
			window = 32768
		}
		definitions := tools.Definitions()
		prepared, stats, budgetErr := contextbudget.Prepare(request, definitions, contextbudget.Limits{MaxWindow: window, ReserveOutput: window / 8})
		if budgetErr != nil {
			return messages, budgetErr
		}
		if stats.DroppedMessages > 0 {
			fmt.Printf("【上下文】本次请求省略了 %d 条较早消息，保留最近的对话。\n", stats.DroppedMessages)
		}
		if input.Interactive() {
			fmt.Println("正在等待模型回复…  Ctrl+C 取消")
		} else {
			fmt.Println("正在等待模型回复…")
		}
		if onRequest != nil {
			onRequest()
		}
		renderer := terminal.NewRenderer(os.Stdout)
		renderer.StartReply()
		m, e := client.Chat(ctx, prepared, definitions, renderer.WriteReply)
		streamed := renderer.EndReply()
		if e != nil {
			if streamed {
				fmt.Println("（回复已中断）")
			}
			return messages, e
		}
		if e = s.AppendMessage(m); e != nil {
			return messages, e
		}
		messages = append(messages, m)
		if len(m.ToolCalls) == 0 {
			if !streamed {
				fmt.Println("模型未返回文本。")
			}
			return messages, nil
		}
		for index, call := range m.ToolCalls {
			if ctx.Err() != nil {
				fmt.Printf("【工具】%s：执行前已取消。\n", call.Function.Name)
				t := provider.Message{Role: "tool", ToolCallID: call.ID, Content: "Cancelled before execution"}
				if e = s.AppendMessage(t); e != nil {
					return messages, e
				}
				messages = append(messages, t)
				continue
			}
			confirm := input.Confirm
			fmt.Printf("\n工具 %d/%d  %s\n", index+1, len(m.ToolCalls), terminal.Safe(call.Function.Name))
			if command := trace.record(call); command != "" {
				fmt.Printf("命令请求：\n%s\n", terminal.Safe(command))
			}
			result := workspace.Execute(ctx, root, mode, call, confirm)
			t := provider.Message{Role: "tool", ToolCallID: call.ID, Content: result}
			if e = s.AppendMessage(t); e != nil {
				return messages, e
			}
			messages = append(messages, t)
			terminal.NewRenderer(os.Stdout).ToolResult(result)
		}
		if ctx.Err() != nil {
			return messages, ctx.Err()
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
