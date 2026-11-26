package main

import (
	"fmt"
	"strconv"
	"strings"

	"kero/internal/provider"
	"kero/internal/session"
	"kero/internal/terminal"
)

// chooseSession lists only sessions belonging to the current workspace.
func chooseSession(input *terminal.Input, root, current string) (string, error) {
	sessions, err := session.List(root)
	if err != nil {
		return "", err
	}
	if len(sessions) == 0 {
		fmt.Println("当前工作区还没有保存的会话。")
		return "", nil
	}
	fmt.Println("当前工作区的会话 · 最近更新在前")
	for i, s := range sessions {
		marker := ""
		if s.ID == current {
			marker = "  [当前]"
		}
		preview := terminal.Safe(s.Preview)
		if preview == "" {
			preview = "尚无用户消息"
		}
		fmt.Printf("%d. %s%s\n   %s\n   %s\n", i+1, s.Updated.Local().Format("2006-01-02 15:04"), marker, preview, s.ID)
	}
	for {
		text, err := input.Prompt("请选择编号（输入 0 取消）：")
		if err != nil {
			return "", err
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		n, err := strconv.Atoi(text)
		if err != nil || n < 0 || n > len(sessions) {
			fmt.Println("编号无效，请重新输入。")
			continue
		}
		if n == 0 {
			fmt.Println("已取消选择会话。")
			return "", nil
		}
		return sessions[n-1].ID, nil
	}
}

func printRecoveredConversation(messages []provider.Message) {
	count := 0
	for _, m := range messages {
		if (m.Role == "user" || m.Role == "assistant") && m.Content != "" {
			count++
		}
	}
	if count == 0 {
		fmt.Println("尚无历史对话")
		return
	}
	fmt.Printf("历史对话 · %d 条\n", count)
	for _, m := range messages {
		if (m.Role == "user" || m.Role == "assistant") && m.Content != "" {
			label := "你"
			if m.Role == "assistant" {
				label = "助手"
			}
			fmt.Printf("\n%s\n%s\n", label, terminal.Safe(m.Content))
		} else if m.Role == "tool" && m.Content == "execution status unknown" {
			fmt.Println("\n工具执行状态未知，请检查工作区。")
		}
	}
	fmt.Println("\n历史结束")
}
