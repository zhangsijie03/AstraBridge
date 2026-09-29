package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const correctionRequest = `{"model":"gpt-6-astra","input":"edit a file","stream":true,"reasoning":{"effort":"ultra"},"tools":[{"type":"custom","name":"exec"}]}`
const correctionPayload = "const svg = `<svg title=\"test\">\\n</svg>`;\nreturn svg;"

// 仅构造本机模拟 BPS 的原生工具帧，不执行文本或调用真实上游。
func writeCorrectionCall(w http.ResponseWriter, id, summary, code string) {
	args, _ := json.Marshal(map[string]any{"code": code, "summary": summary, "extended_summary": "{}", "references": []any{}, "destructive": false})
	call := map[string]any{"type": "function_call", "name": "run_officejs", "call_id": id, "id": "fc_" + id, "arguments": string(args), "status": "completed"}
	data, _ := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_" + id, "status": "completed", "output": []any{call}, "usage": map[string]int{"input_tokens": 10, "output_tokens": 2, "total_tokens": 12}}})
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprintf(w, "event: response.completed\ndata: %s\n\n", data)
}

func TestGatewayNativeCorrectionContinuesHistoryAndPreservesPayload(t *testing.T) {
	var count atomic.Int32
	var firstMetadata map[string]any
	g := gateway(t, func(w http.ResponseWriter, r *http.Request) {
		n := count.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+token() || r.Header.Get("Chatgpt-Account-Id") != "account-1" {
			t.Error("correction changed account")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if body["model"] != "gpt-6-astra" || body["reasoning_effort"] != "xhigh" {
			t.Error("correction changed model/effort")
		}
		metadata := body["metadata"].(map[string]any)
		if n == 1 {
			firstMetadata = metadata
		} else {
			if metadata["task_id"] != firstMetadata["task_id"] || metadata["turn_id"] != firstMetadata["turn_id"] || metadata["agent_iteration"] != fmt.Sprint(n) {
				t.Error("correction lost continuation metadata")
			}
			input := body["input"].([]any)
			feedback := 0
			for _, v := range input {
				item := v.(map[string]any)
				if item["type"] == "function_call_output" {
					feedback++
					if !strings.Contains(item["output"].(string), `"executed":false`) {
						t.Error("invalid call represented as executed")
					}
				}
			}
			if feedback != int(n)-1 {
				t.Errorf("lost correction history: %d", feedback)
			}
		}
		if n < 3 {
			writeCorrectionCall(w, fmt.Sprint(n), "missing marker", correctionPayload)
		} else {
			writeCorrectionCall(w, "3", "codex2api.custom/exec", "model rewrote source")
		}
	})
	w := httptest.NewRecorder()
	g.ServeHTTP(w, request(correctionRequest))
	out := w.Body.String()
	if count.Load() != 3 || !strings.Contains(out, "response.completed") || strings.Contains(out, "response.failed") {
		t.Fatalf("correction failed: %d %s", count.Load(), out)
	}
	encoded, _ := json.Marshal(correctionPayload)
	if !strings.Contains(out, string(encoded)) || strings.Contains(out, "model rewrote source") || strings.Contains(out, "run_officejs") {
		t.Fatalf("raw payload changed/leaked: %s", out)
	}
	if !strings.Contains(out, `"total_tokens":36`) {
		t.Fatalf("usage lost: %s", out)
	}
}

func TestGatewayNativeUnknownTargetCorrectsOnlyOnce(t *testing.T) {
	var calls atomic.Int32
	g := gateway(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			writeCorrectionCall(w, "1", "ordinary", `{"name":"not_declared","arguments":{}}`)
		} else {
			writeCorrectionCall(w, "2", "codex2api.custom/exec", correctionPayload)
		}
	})
	w := httptest.NewRecorder()
	g.ServeHTTP(w, request(correctionRequest))
	if calls.Load() != 2 || !strings.Contains(w.Body.String(), "response.custom_tool_call_input.done") {
		t.Fatalf("unknown target recovery failed: %d %s", calls.Load(), w.Body.String())
	}
}

func TestGatewayNativeCorrectionBoundsAndRejections(t *testing.T) {
	for _, mode := range []string{"exhausted", "unknown_again", "http_401", "non_sse", "incomplete", "schema_invalid"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			g := gateway(t, func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				if mode == "unknown_again" {
					writeCorrectionCall(w, fmt.Sprint(n), "ordinary", `{"name":"missing","arguments":{}}`)
					return
				}
				if mode == "schema_invalid" {
					writeCorrectionCall(w, "1", "ordinary", `{"name":"shell","arguments":{"count":"invalid"}}`)
					return
				}
				if n == 1 || mode == "exhausted" {
					writeCorrectionCall(w, fmt.Sprint(n), "missing marker", correctionPayload)
					return
				}
				switch mode {
				case "http_401":
					w.WriteHeader(401)
					fmt.Fprint(w, "private-upstream-error")
				case "non_sse":
					w.Header().Set("Content-Type", "text/html")
					fmt.Fprint(w, "private-upstream-error")
				case "incomplete":
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"type\":\"response.created\",\"response\":{}}\n\n")
				}
			})
			body := correctionRequest
			want := int32(2)
			if mode == "exhausted" {
				want = 3
			}
			if mode == "schema_invalid" {
				want = 1
				body = `{"model":"gpt-6-astra","input":"test","stream":true,"tools":[{"type":"function","name":"shell","parameters":{"type":"object","properties":{"count":{"type":"integer"}},"required":["count"]}}]}`
			}
			w := httptest.NewRecorder()
			g.ServeHTTP(w, request(body))
			out := w.Body.String()
			if calls.Load() != want || !strings.Contains(out, "response.failed") || strings.Contains(out, "response.output_item.added") || strings.Contains(out, "private-upstream-error") {
				t.Fatalf("invalid batch dispatched/retried/leaked: %d %s", calls.Load(), out)
			}
		})
	}
}

func TestGatewayCancellationStopsNativeCorrection(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	var calls atomic.Int32
	g := gateway(t, func(w http.ResponseWriter, r *http.Request) {
		// 读完请求体后 HTTP 服务端才开始监听连接断开。
		_, _ = io.Copy(io.Discard, r.Body)
		if calls.Add(1) == 1 {
			writeCorrectionCall(w, "1", "missing marker", correctionPayload)
			return
		}
		close(started)
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
			return
		}
		close(stopped)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		g.ServeHTTP(httptest.NewRecorder(), request(correctionRequest).WithContext(ctx))
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("correction did not start")
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("correction HTTP request did not cancel")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("gateway did not finish cancellation")
	}
	if calls.Load() != 2 {
		t.Fatal("cancelled correction was retried")
	}
}
