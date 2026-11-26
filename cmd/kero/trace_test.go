package main

import (
	"bytes"
	"strings"
	"testing"

	"kero/internal/provider"
)

func TestCommandTraceRecordsRequestsWithoutClaimingExecution(t *testing.T) {
	var trace commandTrace
	trace.record(provider.ToolCall{Function: provider.FunctionCall{Name: "read_file", Arguments: `{"path":"a"}`}})
	trace.record(provider.ToolCall{Function: provider.FunctionCall{Name: "run_command", Arguments: `{"command":"go test ./..."}`}})
	trace.record(provider.ToolCall{Function: provider.FunctionCall{Name: "run_command", Arguments: `{"command":"echo \u001b[2J"}`}})
	trace.record(provider.ToolCall{Function: provider.FunctionCall{Name: "run_command", Arguments: `bad`}})
	var out bytes.Buffer
	trace.print(&out)
	text := out.String()
	if !strings.Contains(text, "go test ./...") || !strings.Contains(text, "echo ") || strings.ContainsRune(text, '\x1b') || !strings.Contains(text, "是否执行") {
		t.Fatalf("unexpected trace: %q", text)
	}
	if len(trace.requests) != 2 {
		t.Fatalf("recorded %d requests", len(trace.requests))
	}
}
