package basispoints

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStreamAddsOutputTextWireFields(t *testing.T) {
	source := testSource()
	source["stream"] = true
	_, bridge := mustPrepare(t, source, "output-wire", nil)

	message := object{
		"type": "message",
		"id":   "msg_output_wire",
		"role": "assistant",
		"content": []any{object{
			"type":        "output_text",
			"text":        "file result",
			"annotations": []any{object{"type": "file_citation", "file_id": "file-1", "filename": "二期开发计划.md", "index": json.Number("955")}},
		}},
	}
	wire := sse(object{"type": "response.content_part.added", "output_index": 0, "content_index": 0, "part": object{"type": "output_text", "text": "file result"}}) +
		sse(object{"type": "response.output_item.added", "output_index": 0, "item": message}) +
		structuredTerminal("response.completed", message)
	events := structuredEvents(t, bridge, wire)

	partEvent := events[0]["part"].(object)
	require.Equal(t, []any{}, partEvent["annotations"])
	require.Equal(t, []any{}, partEvent["logprobs"])

	item := events[1]["item"].(object)
	part := item["content"].([]any)[0].(object)
	require.Equal(t, []any{object{"type": "file_citation", "file_id": "file-1", "filename": "二期开发计划.md", "index": json.Number("955")}}, part["annotations"])
	require.Equal(t, []any{}, part["logprobs"])

	terminal := events[len(events)-1]["response"].(object)
	terminalItem := terminal["output"].([]any)[0].(object)
	terminalPart := terminalItem["content"].([]any)[0].(object)
	require.Equal(t, []any{object{"type": "file_citation", "file_id": "file-1", "filename": "二期开发计划.md", "index": json.Number("955")}}, terminalPart["annotations"])
	require.Equal(t, []any{}, terminalPart["logprobs"])
}
