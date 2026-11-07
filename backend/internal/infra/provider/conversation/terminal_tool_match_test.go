package conversation

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/chenyme/grok2api/backend/internal/infra/provider"
)

type messagesToolUse struct {
	id         string
	input      string
	stops      int
	stopReason string
}

func singleMessagesToolUse(t *testing.T, stream string) messagesToolUse {
	t.Helper()
	var tool messagesToolUse
	toolIndex := -1
	scanner := bufio.NewScanner(strings.NewReader(stream))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event struct {
			Type         string `json:"type"`
			Index        int    `json:"index"`
			ContentBlock struct {
				Type string `json:"type"`
				ID   string `json:"id"`
			} `json:"content_block"`
			Delta struct {
				Type        string `json:"type"`
				PartialJSON string `json:"partial_json"`
				StopReason  string `json:"stop_reason"`
			} `json:"delta"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event) != nil {
			continue
		}
		switch {
		case event.Type == "content_block_start" && event.ContentBlock.Type == "tool_use":
			if toolIndex >= 0 {
				t.Fatalf("second tool_use block %d after block %d\n%s", event.Index, toolIndex, stream)
			}
			toolIndex, tool.id = event.Index, event.ContentBlock.ID
		case event.Type == "content_block_delta" && event.Delta.Type == "input_json_delta" && event.Index == toolIndex:
			tool.input += event.Delta.PartialJSON
		case event.Type == "content_block_stop" && event.Index == toolIndex:
			tool.stops++
		case event.Type == "message_delta":
			tool.stopReason = event.Delta.StopReason
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if toolIndex < 0 {
		t.Fatalf("no tool_use block\n%s", stream)
	}
	return tool
}

func terminalEnvelopeStream(added, output string) string {
	return strings.Join([]string{
		`event: response.output_item.added`,
		`data: {"type":"response.output_item.added","output_index":1,"item":` + added + `}`, "",
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"id":"resp_shifted","status":"completed","output":[` + output + `]}}`, "",
	}, "\n")
}

func TestMessagesTerminalEnvelopeReusesStreamedToolUse(t *testing.T) {
	for _, test := range []struct {
		name   string
		added  string
		output string
		wantID string
	}{
		{
			name:   "terminal call_id without item id",
			added:  `{"id":"fc_1","type":"function_call","call_id":"call_1","name":"lookup","arguments":""}`,
			output: `{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":1}"}`,
			wantID: "toolu_call_1",
		},
		{
			name:   "no ids",
			added:  `{"type":"function_call","name":"lookup","arguments":""}`,
			output: `{"type":"function_call","name":"lookup","arguments":"{\"q\":1}"}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			stream := terminalEnvelopeStream(test.added, test.output)
			body, err := io.ReadAll(ConvertResponseStreamWithOptions(io.NopCloser(strings.NewReader(stream)), OperationMessages, ResponseOptions{}))
			if err != nil {
				t.Fatal(err)
			}
			text := string(body)
			if types := contentBlockStartTypes(t, text); len(types) != 1 || types[0] != "tool_use" {
				t.Fatalf("content blocks = %v, want one tool_use\n%s", types, text)
			}
			assertSequentialContentBlocks(t, text)
			tool := singleMessagesToolUse(t, text)
			if test.wantID != "" && tool.id != test.wantID {
				t.Fatalf("tool_use id = %q, want %q", tool.id, test.wantID)
			}
			if !strings.HasPrefix(tool.id, "toolu_") || tool.input != `{"q":1}` || tool.stops != 1 || tool.stopReason != "tool_use" {
				t.Fatalf("tool_use = %#v\n%s", tool, text)
			}
		})
	}
}

func TestChatTerminalEnvelopeMatchesStreamedCallByCallID(t *testing.T) {
	stream := terminalEnvelopeStream(
		`{"id":"fc_1","type":"function_call","call_id":"call_1","name":"lookup","arguments":""}`,
		`{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":1}"}`,
	)
	body, err := io.ReadAll(ConvertResponseStreamWithOptions(io.NopCloser(strings.NewReader(stream)), OperationChat, ResponseOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if strings.Count(text, `"name":"lookup"`) != 1 || strings.Count(text, `"id":"call_1"`) != 1 || !strings.Contains(text, `\"q\":1`) {
		t.Fatalf("terminal envelope duplicated the streamed call: %s", text)
	}
}

func TestAnthropicWebSearchQueryWithRunesThatChangeLengthWhenLowered(t *testing.T) {
	for _, prefix := range []string{"ȺȺȺȺ ", "\u212a\u212a\u212a\u212a "} {
		content, _ := json.Marshal(prefix + "Perform a web search for the query: rust tutorials")
		if got := anthropicWebSearchQuery([]anthropicMessage{{Role: "user", Content: content}}); got != "rust tutorials" {
			t.Fatalf("prefix %q: query = %q", prefix, got)
		}
	}
}

type panickingSource struct{}

func (panickingSource) Read([]byte) (int, error) { panic("source exploded") }

func (panickingSource) Close() error { return nil }

func TestConvertedStreamsRecoverProducerPanics(t *testing.T) {
	for _, operation := range []string{OperationChat, OperationMessages, OperationResponses} {
		_, err := io.ReadAll(ConvertResponseStreamWithOptions(panickingSource{}, operation, ResponseOptions{}))
		if !errors.Is(err, provider.ErrStreamPanic) {
			t.Fatalf("%s: err = %v, want ErrStreamPanic", operation, err)
		}
	}
}
