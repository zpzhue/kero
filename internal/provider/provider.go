package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type Tool struct {
	Type     string         `json:"type"`
	Function ToolDefinition `json:"function"`
}

type ToolDefinition struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

type Client struct {
	BaseURL string
	APIKey  string
	Model   string
}

// Chat streams an assistant response and calls onText for each text fragment.
func (c *Client) Chat(ctx context.Context, messages []Message, tools []Tool, onText func(string)) (Message, error) {
	var result Message
	body, err := json.Marshal(struct {
		Model    string    `json:"model"`
		Messages []Message `json:"messages"`
		Tools    []Tool    `json:"tools,omitempty"`
		Stream   bool      `json:"stream"`
	}{c.Model, messages, tools, true})
	if err != nil {
		return result, fmt.Errorf("encode chat request: %w", err)
	}
	endpoint := strings.TrimRight(c.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return result, fmt.Errorf("create chat request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return result, fmt.Errorf("chat request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if readErr != nil {
			return result, fmt.Errorf("chat request returned %s: %w", resp.Status, readErr)
		}
		return result, fmt.Errorf("chat request returned %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}

	result.Role = "assistant"
	calls := make(map[int]*ToolCall)
	var content strings.Builder
	finished := false
	done := false
	var eventType string
	var dataLines []string
	process := func() error {
		if len(dataLines) == 0 {
			eventType = ""
			return nil
		}
		data := strings.Join(dataLines, "\n")
		dataLines = nil
		kind := eventType
		eventType = ""
		if data == "[DONE]" {
			done = true
			return nil
		}
		if kind == "error" {
			return fmt.Errorf("chat stream error: %s", data)
		}
		var chunk struct {
			Error   json.RawMessage `json:"error"`
			Choices []struct {
				Index        int     `json:"index"`
				FinishReason *string `json:"finish_reason"`
				Delta        struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int          `json:"index"`
						ID       string       `json:"id"`
						Type     string       `json:"type"`
						Function FunctionCall `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return fmt.Errorf("decode chat stream: %w", err)
		}
		if len(chunk.Error) != 0 && string(chunk.Error) != "null" {
			return fmt.Errorf("chat stream error: %s", chunk.Error)
		}
		for _, choice := range chunk.Choices {
			if choice.Index != 0 {
				continue
			}
			if choice.Delta.Content != "" {
				content.WriteString(choice.Delta.Content)
				if onText != nil {
					onText(choice.Delta.Content)
				}
			}
			for _, fragment := range choice.Delta.ToolCalls {
				call := calls[fragment.Index]
				if call == nil {
					call = &ToolCall{}
					calls[fragment.Index] = call
				}
				call.ID += fragment.ID
				call.Type += fragment.Type
				call.Function.Name += fragment.Function.Name
				call.Function.Arguments += fragment.Function.Arguments
			}
			if choice.FinishReason != nil {
				finished = true
			}
		}
		return nil
	}

	scanner := bufio.NewScanner(resp.Body)
	// Tool arguments can contain large JSON strings; allow events up to 16 MiB.
	scanner.Buffer(make([]byte, 4096), 16<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := process(); err != nil {
				return Message{}, err
			}
			if done {
				break
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, found := strings.Cut(line, ":")
		if !found {
			value = ""
		}
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "data":
			dataLines = append(dataLines, value)
		case "event":
			eventType = value
		}
	}
	if err := scanner.Err(); err != nil {
		return Message{}, fmt.Errorf("read chat stream: %w", err)
	}
	if !done && len(dataLines) != 0 {
		if err := process(); err != nil {
			return Message{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return Message{}, err
	}
	if !done && !finished {
		return Message{}, errors.New("chat stream ended before completion")
	}
	result.Content = content.String()
	indices := make([]int, 0, len(calls))
	for index := range calls {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, index := range indices {
		result.ToolCalls = append(result.ToolCalls, *calls[index])
	}
	return result, nil
}
