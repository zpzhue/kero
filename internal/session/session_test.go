package session

import (
	"kero/internal/provider"
	"os"
	"runtime"
	"testing"
)

func TestInterruptedToolIsNotReplayed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Setenv("APPDATA", t.TempDir())
	} else if runtime.GOOS == "darwin" {
		t.Setenv("HOME", t.TempDir())
	} else {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	}
	root := t.TempDir()
	s, _, err := Open("", root)
	if err != nil {
		t.Fatal(err)
	}
	id := s.ID()
	call := provider.ToolCall{ID: "call1", Type: "function", Function: provider.FunctionCall{Name: "run_command", Arguments: `{"command":"echo hi"}`}}
	if err = s.AppendMessage(provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{call}}); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, messages, err := Open(id, root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if len(messages) != 2 || messages[1].Role != "tool" || messages[1].ToolCallID != "call1" || messages[1].Content != "execution status unknown" {
		t.Fatalf("incorrect recovery: %#v", messages)
	}
	if _, err = os.Stat(s.file.Name()); err != nil {
		t.Fatal(err)
	}
}
