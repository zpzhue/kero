// Package contextbudget prepares bounded provider requests from conversation history.
package contextbudget

import (
	"encoding/json"
	"fmt"

	"kero/internal/provider"
)

const (
	requestOverhead        = 8
	messageOverhead        = 8
	toolCallOverhead       = 8
	toolDefinitionOverhead = 8
)

// Limits specifies the total context window and the tokens reserved for output.
// MaxWindow must be positive and ReserveOutput must be in [0, MaxWindow).
type Limits struct {
	MaxWindow     int
	ReserveOutput int
}

// Stats describes the prepared request, including its system messages and tools.
type Stats struct {
	EstimatedInputTokens int
	InputTokenBudget     int
	DroppedMessages      int
}

// Prepare keeps system messages and the longest suffix of whole turns that fits.
// A user message starts a turn, which includes any following assistant messages,
// tool calls, and tool results up to the next user message. Conversation before
// the first user message is grouped together. The latest turn may be awaiting
// an assistant response, but all tool calls must have matching results.
//
// Estimates charge one token per UTF-8 byte, plus framing allowances. Prepare
// leaves history unchanged. The returned messages may share storage with the
// input and are intended for read-only use in a provider request.
func Prepare(messages []provider.Message, tools []provider.Tool, limits Limits) ([]provider.Message, Stats, error) {
	if limits.MaxWindow <= 0 || limits.ReserveOutput < 0 || limits.ReserveOutput >= limits.MaxWindow {
		return nil, Stats{}, fmt.Errorf("invalid context limits: maximum window %d, output reserve %d", limits.MaxWindow, limits.ReserveOutput)
	}

	stats := Stats{
		InputTokenBudget:     limits.MaxWindow - limits.ReserveOutput,
		EstimatedInputTokens: requestOverhead,
	}
	if len(tools) > 0 {
		encoded, err := json.Marshal(tools)
		if err != nil {
			return nil, Stats{}, fmt.Errorf("estimate tool definitions: %w", err)
		}
		stats.EstimatedInputTokens += len(encoded) + toolDefinitionOverhead
	}

	systemCount := 0
	for _, message := range messages {
		if message.Role == "system" {
			systemCount++
			stats.EstimatedInputTokens += estimateMessage(message)
		}
	}
	if stats.EstimatedInputTokens > stats.InputTokenBudget {
		return nil, Stats{}, fmt.Errorf("system messages and tool definitions require an estimated %d input tokens, exceeding the budget of %d", stats.EstimatedInputTokens, stats.InputTokenBudget)
	}
	if err := validateToolGroups(messages); err != nil {
		return nil, Stats{}, err
	}

	firstKept := len(messages)
	keptSystems := 0
	for firstKept > 0 {
		start := firstKept
		turnTokens, turnSystems := 0, 0
		for start > 0 {
			start--
			message := messages[start]
			if message.Role == "system" {
				turnSystems++
				continue
			}
			turnTokens += estimateMessage(message)
			if message.Role == "user" {
				break
			}
		}
		if turnTokens > stats.InputTokenBudget-stats.EstimatedInputTokens {
			if firstKept == len(messages) {
				return nil, Stats{}, fmt.Errorf("latest turn requires an estimated %d input tokens with system messages and tool definitions, exceeding the budget of %d", turnTokens+stats.EstimatedInputTokens, stats.InputTokenBudget)
			}
			break
		}
		stats.EstimatedInputTokens += turnTokens
		keptSystems += turnSystems
		firstKept = start
	}

	prefixSystems := systemCount - keptSystems
	stats.DroppedMessages = firstKept - prefixSystems
	if stats.DroppedMessages == 0 {
		return messages, stats, nil
	}
	if prefixSystems == 0 {
		return messages[firstKept:], stats, nil
	}
	prepared := make([]provider.Message, 0, len(messages)-stats.DroppedMessages)
	for _, message := range messages[:firstKept] {
		if message.Role == "system" {
			prepared = append(prepared, message)
		}
	}
	return append(prepared, messages[firstKept:]...), stats, nil
}

func estimateMessage(message provider.Message) int {
	tokens := messageOverhead + len(message.Role) + len(message.Content) + len(message.ToolCallID)
	for _, call := range message.ToolCalls {
		tokens += toolCallOverhead + len(call.ID) + len(call.Type) + len(call.Function.Name) + len(call.Function.Arguments)
	}
	return tokens
}

func validateToolGroups(messages []provider.Message) error {
	pending := make(map[string]struct{})
	for i, message := range messages {
		if message.Role == "tool" {
			if message.ToolCallID == "" || len(message.ToolCalls) != 0 {
				return fmt.Errorf("invalid tool result at message %d", i+1)
			}
			if _, exists := pending[message.ToolCallID]; !exists {
				return fmt.Errorf("tool result at message %d has no pending call for ID %q", i+1, message.ToolCallID)
			}
			delete(pending, message.ToolCallID)
			continue
		}
		if len(pending) != 0 {
			return fmt.Errorf("%d tool call(s) still need results before message %d", len(pending), i+1)
		}
		if message.Role == "" || message.ToolCallID != "" || len(message.ToolCalls) != 0 && message.Role != "assistant" {
			return fmt.Errorf("invalid chat message at position %d", i+1)
		}
		for _, call := range message.ToolCalls {
			if call.ID == "" {
				return fmt.Errorf("tool call at message %d has no ID", i+1)
			}
			if _, exists := pending[call.ID]; exists {
				return fmt.Errorf("duplicate tool call ID %q at message %d", call.ID, i+1)
			}
			pending[call.ID] = struct{}{}
		}
	}
	if len(pending) != 0 {
		return fmt.Errorf("latest turn has %d tool call(s) without results", len(pending))
	}
	return nil
}
