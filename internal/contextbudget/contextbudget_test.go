package contextbudget

import (
	"reflect"
	"strings"
	"testing"

	"kero/internal/provider"
)

func TestPrepareKeepsWholeTurnsAndSystemMessages(t *testing.T) {
	system := provider.Message{Role: "system", Content: "Follow the workspace instructions."}
	older := []provider.Message{
		{Role: "user", Content: "Inspect the workspace."},
		{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "inspect"}}},
		{Role: "tool", ToolCallID: "inspect", Content: strings.Repeat("file list ", 100)},
		{Role: "assistant", Content: "I inspected the workspace."},
	}
	latest := []provider.Message{
		{Role: "user", Content: "Read both files."},
		{Role: "assistant", ToolCalls: []provider.ToolCall{
			{ID: "first", Type: "function", Function: provider.FunctionCall{Name: "read", Arguments: `{"path":"first.go"}`}},
			{ID: "second", Type: "function", Function: provider.FunctionCall{Name: "read", Arguments: `{"path":"second.go"}`}},
		}},
		{Role: "tool", ToolCallID: "second", Content: "Second file contents"},
		{Role: "tool", ToolCallID: "first", Content: "First file contents"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "verify"}}},
		{Role: "tool", ToolCallID: "verify", Content: "Verified"},
	}
	tools := []provider.Tool{{Type: "function", Function: provider.ToolDefinition{
		Name: "read", Parameters: map[string]any{"type": "object"},
	}}}
	want := append([]provider.Message{system}, latest...)
	_, exact, err := Prepare(want, tools, Limits{MaxWindow: 4096})
	if err != nil {
		t.Fatal(err)
	}
	// Leave room for the old final assistant message, but only as a fragment of its turn.
	inputBudget := exact.EstimatedInputTokens + estimateMessage(older[len(older)-1])
	const reserve = 64
	messages := append([]provider.Message{system}, older...)
	messages = append(messages, latest...)
	original := append([]provider.Message(nil), messages...)
	got, stats, err := Prepare(messages, tools, Limits{MaxWindow: inputBudget + reserve, ReserveOutput: reserve})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("prepared messages = %#v, want %#v", got, want)
	}
	if stats.EstimatedInputTokens != exact.EstimatedInputTokens || stats.InputTokenBudget != inputBudget || stats.DroppedMessages != len(older) {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	if !reflect.DeepEqual(messages, original) {
		t.Fatal("Prepare changed the input history")
	}
}

func TestPrepareKeepsContiguousSuffix(t *testing.T) {
	firstSystem := provider.Message{Role: "system", Content: "First instruction."}
	secondSystem := provider.Message{Role: "system", Content: "Second instruction."}
	latest := provider.Message{Role: "user", Content: "Continue."}
	messages := []provider.Message{
		firstSystem,
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi"},
		{Role: "user", Content: strings.Repeat("long context ", 100)},
		secondSystem,
		{Role: "assistant", Content: "Done"},
		latest,
	}
	want := []provider.Message{firstSystem, secondSystem, latest}
	_, minimum, err := Prepare(want, nil, Limits{MaxWindow: 4096})
	if err != nil {
		t.Fatal(err)
	}
	earlierTurn := estimateMessage(messages[1]) + estimateMessage(messages[2])
	got, stats, err := Prepare(messages, nil, Limits{MaxWindow: minimum.EstimatedInputTokens + earlierTurn})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) || stats.DroppedMessages != 4 {
		t.Fatalf("prepared messages = %#v, stats = %+v", got, stats)
	}
}

func TestPrepareBudgetsUTF8AndToolDefinitions(t *testing.T) {
	ascii := []provider.Message{{Role: "user", Content: "abc"}}
	unicode := []provider.Message{{Role: "user", Content: "界界界"}}
	limits := Limits{MaxWindow: 4096, ReserveOutput: 64}
	_, asciiStats, err := Prepare(ascii, nil, limits)
	if err != nil {
		t.Fatal(err)
	}
	_, unicodeStats, err := Prepare(unicode, nil, limits)
	if err != nil {
		t.Fatal(err)
	}
	if difference := unicodeStats.EstimatedInputTokens - asciiStats.EstimatedInputTokens; difference != 6 {
		t.Fatalf("UTF-8 input cost increased by %d, want 6", difference)
	}
	tools := []provider.Tool{{Type: "function", Function: provider.ToolDefinition{
		Name: "write", Parameters: map[string]any{"description": strings.Repeat("a", 100)},
	}}}
	_, withTools, err := Prepare(unicode, tools, limits)
	if err != nil {
		t.Fatal(err)
	}
	if withTools.EstimatedInputTokens-unicodeStats.EstimatedInputTokens < 100 {
		t.Fatalf("tool schema was undercounted: %+v", withTools)
	}

	limits.MaxWindow = withTools.EstimatedInputTokens + limits.ReserveOutput
	if _, _, err := Prepare(unicode, tools, limits); err != nil {
		t.Fatalf("input with an exact budget should fit: %v", err)
	}
	limits.MaxWindow--
	got, _, err := Prepare(unicode, tools, limits)
	if got != nil || err == nil || !strings.Contains(err.Error(), "latest turn") {
		t.Fatalf("one token short: got messages %#v, error %v", got, err)
	}
}

func TestPrepareRejectsInvalidToolGroups(t *testing.T) {
	call := provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "read"}}}
	result := provider.Message{Role: "tool", ToolCallID: "read"}
	tests := []struct {
		name     string
		messages []provider.Message
		want     string
	}{
		{"orphan result", []provider.Message{result}, "no pending call"},
		{"missing result", []provider.Message{call}, "without results"},
		{"repeated result", []provider.Message{call, result, result}, "no pending call"},
		{"interrupted group", []provider.Message{call, {Role: "user"}, result}, "need results before message"},
		{"duplicate call ID", []provider.Message{{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "read"}, {ID: "read"}}}}, "duplicate tool call ID"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _, err := Prepare(tt.messages, nil, Limits{MaxWindow: 4096})
			if got != nil || err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Prepare returned messages %#v, error %v, want an error containing %q", got, err, tt.want)
			}
		})
	}
}
