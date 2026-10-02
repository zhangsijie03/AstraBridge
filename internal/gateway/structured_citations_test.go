package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGatewayPreservesStructuredCitations(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, format := range []string{"json_object", "json_schema", "text", "default"} {
			t.Run(fmt.Sprintf("%s/stream=%t", format, stream), func(t *testing.T) {
				path := filepath.ToSlash(filepath.Join(t.TempDir(), "report [1].txt"))
				require.NoError(t, os.WriteFile(path, []byte("synthetic report"), 0600))
				citation := fmt.Sprintf("【report[1]:1】 (<%s:1>)", path)
				// 模型可以输出不转义的尖括号。使用跨平台路径，确保 Windows 也真正触发引用改写。
				answer := `{"reference":"` + citation + `"}`
				require.True(t, json.Valid([]byte(answer)))
				g := gateway(t, func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, citationEventFrame(t, map[string]any{
						"type": "response.completed", "response": map[string]any{
							"id": "synthetic", "status": "completed", "output": []any{map[string]any{
								"id": "message-1", "type": "message", "role": "assistant", "status": "completed",
								"content": []any{map[string]any{"type": "output_text", "text": answer}},
							}},
						},
					}))
				})
				g.SetLocalFileBaseURL("http://127.0.0.1:17861")
				requested := map[string]any{"type": format}
				if format == "json_schema" {
					requested["name"], requested["strict"] = "reference", true
					requested["schema"] = map[string]any{
						"type": "object", "properties": map[string]any{"reference": map[string]any{"type": "string", "const": citation}},
						"required": []string{"reference"}, "additionalProperties": false,
					}
				}
				body := map[string]any{"model": "gpt-6-astra", "input": "synthetic", "stream": stream}
				if format != "default" {
					body["text"] = map[string]any{"format": requested}
				}
				raw, err := json.Marshal(body)
				require.NoError(t, err)
				response := httptest.NewRecorder()
				g.ServeHTTP(response, request(string(raw)))
				require.Equal(t, http.StatusOK, response.Code)
				var final map[string]any
				var deltas strings.Builder
				var snapshots []string
				if stream {
					sequence := 0
					for _, event := range citationEvents(t, response.Body.String()) {
						if event["type"] == "keepalive" {
							continue
						}
						require.Equal(t, float64(sequence), event["sequence_number"])
						sequence++
						switch event["type"] {
						case "response.completed":
							final = event["response"].(map[string]any)
						case "response.output_text.delta":
							deltas.WriteString(event["delta"].(string))
						case "response.output_text.done":
							snapshots = append(snapshots, event["text"].(string))
						case "response.content_part.done":
							snapshots = append(snapshots, event["part"].(map[string]any)["text"].(string))
						}
					}
				} else {
					require.NoError(t, json.Unmarshal(response.Body.Bytes(), &final))
				}
				require.NotNil(t, final, response.Body.String())
				require.Equal(t, "completed", final["status"])
				item := final["output"].([]any)[0].(map[string]any)
				text := item["content"].([]any)[0].(map[string]any)["text"].(string)
				if format == "text" || format == "default" {
					require.Contains(t, text, "/v1/files/", "ordinary text must still get clickable citations")
					require.NotEmpty(t, g.fileBroker.grants)
				} else {
					require.Equal(t, answer, text, "validated structured text must remain unchanged")
					var decoded map[string]string
					require.NoError(t, json.Unmarshal([]byte(text), &decoded))
					require.Equal(t, citation, decoded["reference"], "preserve the const constraint")
					require.Empty(t, g.fileBroker.grants, "structured outputs must not grant local file access")
					if stream {
						require.Equal(t, answer, deltas.String())
						require.NotEmpty(t, snapshots)
						for _, snapshot := range snapshots {
							require.Equal(t, answer, snapshot)
						}
					}
				}
			})
		}
	}
}

func TestCitationPaddingPreservesLinksAtEverySplit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report with spaces.txt")
	require.NoError(t, os.WriteFile(path, []byte("synthetic report"), 0600))
	for _, padding := range []string{"", " ", "\t", "\r\n", " \t\n"} {
		value := fmt.Sprintf("请读【report:1】 (< %s:1%s>)。", path, padding)
		runes := []rune(value)
		for split := 0; split <= len(runes); split++ {
			r := newCitationRewriter(newLocalFileBroker(), "http://127.0.0.1:17861")
			want := r.rewrite(value)
			require.Contains(t, want, "/v1/files/", "padding %q", padding)
			got := r.feed("0:0", string(runes[:split])) + r.feed("0:0", string(runes[split:]))
			require.Equal(t, want, got, "padding %q split %d", padding, split)
			require.Empty(t, r.pending)
		}
	}
}
