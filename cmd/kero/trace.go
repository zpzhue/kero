package main

import (
	"encoding/json"
	"fmt"
	"io"

	"kero/internal/provider"
	"kero/internal/terminal"
)

type commandTrace struct{ requests []string }

func (t *commandTrace) record(call provider.ToolCall) string {
	if call.Function.Name != "run_command" {
		return ""
	}
	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil || args.Command == "" {
		return ""
	}
	t.requests = append(t.requests, args.Command)
	return args.Command
}

func (t *commandTrace) print(w io.Writer) {
	if len(t.requests) == 0 {
		return
	}
	fmt.Fprintln(w, "\n本轮命令请求（是否执行及执行结果以上方工具结果为准）：")
	for i, command := range t.requests {
		fmt.Fprintf(w, "%d. %s\n", i+1, terminal.Safe(command))
	}
}
