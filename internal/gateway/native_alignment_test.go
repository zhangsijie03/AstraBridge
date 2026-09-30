package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestNativeSessionSignalsAndExecutionLanes(t *testing.T) {
	scope := func(headers map[string]string, body string) string {
		r := request(body)
		r.Header.Del("thread-id")
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		var source map[string]json.RawMessage
		require.NoError(t, json.Unmarshal([]byte(body), &source))
		return requestScope(r, source, "account")
	}
	root := scope(map[string]string{"thread-id": " thread "}, `{}`)
	for _, input := range []struct {
		headers map[string]string
		body    string
	}{
		{map[string]string{turnMetadataKey: `{"thread_id":"thread","request_kind":"turn"}`}, `{}`},
		{map[string]string{"x-codex-window-id": "thread:window"}, `{}`},
		{nil, `{"client_metadata":{"thread_id":"thread"}}`},
		{nil, `{"client_metadata":{"x-codex-turn-metadata":"{\"thread_id\":\"thread\",\"request_kind\":\"compaction\"}"}}`},
	} {
		require.Equal(t, root, scope(input.headers, input.body))
	}
	require.NotEqual(t, root, scope(map[string]string{turnMetadataKey: `{"thread_id":"thread","request_kind":"memory"}`}, `{}`))
	require.Equal(t, root, scope(map[string]string{"thread-id": "thread", "session_id": "shared-parent"}, `{}`))
	session := scope(map[string]string{"session-id": "session"}, `{}`)
	require.NotEmpty(t, session)
	for _, header := range sessionHeaders {
		require.Equal(t, session, scope(map[string]string{header: "session"}, `{}`))
	}
	require.Equal(t, session, scope(nil, `{"prompt_cache_key":"session"}`))
	require.NotEqual(t, session, scope(map[string]string{"session-id": "session", subagentKey: "guardian"}, `{}`))
	require.Empty(t, scope(nil, `{"input":"same content"}`))
	r := request(`{}`)
	a := requestScope(r, nil, "account")
	r.Header.Set("Authorization", "Bearer different")
	require.NotEqual(t, a, requestScope(r, nil, "account"))
}

func TestNativeCatalogInheritanceReplacementAndIsolation(t *testing.T) {
	var wire string
	g := gateway(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		wire = string(raw)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, completed)
	})
	send := func(body, thread string) string {
		r := request(body)
		r.Header.Set("thread-id", thread)
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r)
		require.Equal(t, 200, w.Code, w.Body.String())
		require.Contains(t, w.Body.String(), "response.completed")
		return wire
	}
	first := send(correctionRequest, "a")
	require.Contains(t, first, "codex2api.custom/exec")
	cache := gjson.Get(first, "prompt_cache_key").String()
	require.NotEmpty(t, cache)
	next := send(simpleRequest, "a")
	require.Contains(t, next, "codex2api.custom/exec")
	require.Equal(t, cache, gjson.Get(next, "prompt_cache_key").String())
	require.NotContains(t, send(simpleRequest, "b"), "codex2api.custom/exec")
	require.NotContains(t, send(simpleRequest, ""), "codex2api.custom/exec")
	require.NotContains(t, send(strings.Replace(simpleRequest, `"input":"hello"`, `"input":"hello","tools":[]`, 1), "a"), "codex2api.custom/exec")
	require.NotContains(t, send(simpleRequest, "a"), "codex2api.custom/exec")
}

func TestNativeMixedImagesRetainCatalogAfterUpload(t *testing.T) {
	data, _ := inlinePNG(t)
	var uploads, responses int
	g := imageGateway(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/basispoints/api/attachments" {
			uploads++
			fmt.Fprint(w, `{"openai_file_id":"file-user-image"}`)
			return
		}
		responses++
		raw, _ := io.ReadAll(r.Body)
		require.Contains(t, string(raw), "codex2api.custom/exec")
		if responses > 1 {
			require.Contains(t, string(raw), `"file_id":"file-user-image"`)
			found := false
			for _, item := range gjson.GetBytes(raw, "input").Array() {
				if item.Get("type").String() == "function_call_output" {
					found = true
					require.Equal(t, data, item.Get("output.0.image_url").String())
					require.False(t, item.Get("output.0.file_id").Exists())
				}
			}
			require.True(t, found)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, completed)
	})
	w := httptest.NewRecorder()
	g.ServeHTTP(w, request(correctionRequest))
	require.Equal(t, 200, w.Code)
	body, _ := json.Marshal(map[string]any{"model": "gpt-6-astra", "stream": true, "input": []any{
		map[string]any{"role": "user", "content": []any{imagePart(data)}},
		map[string]any{"type": "function_call", "name": "view_image", "call_id": "image", "arguments": "{}"},
		map[string]any{"type": "function_call_output", "call_id": "image", "output": []any{imagePart(data)}},
	}})
	w = httptest.NewRecorder()
	g.ServeHTTP(w, request(string(body)))
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "response.completed")
	require.Equal(t, 1, uploads)
	require.Equal(t, 2, responses)
}

func TestNativeTerminalsRemainDistinct(t *testing.T) {
	for _, kind := range []string{"response.failed", "response.incomplete", "error"} {
		t.Run(kind, func(t *testing.T) {
			wire := fmt.Sprintf("event: %s\ndata: {\"type\":%q,\"response\":{\"id\":\"resp_native\",\"status\":\"failed\",\"output\":[],\"error\":{\"code\":\"basispoints_protocol_error\",\"message\":\"native diagnostic\"}}}\n\n", kind, kind)
			g := gateway(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, wire)
			})
			traces := new(traceCapture)
			g.SetTraceObserver(traces.add)
			w := httptest.NewRecorder()
			g.ServeHTTP(w, request(simpleRequest))
			require.Contains(t, w.Body.String(), "event: "+kind)
			require.Contains(t, w.Body.String(), "basispoints_protocol_error")
			// v2.9.4 将独立 error 事件归一化为安全错误，不保留非标准 response 外壳。
			if kind != "error" {
				require.Contains(t, w.Body.String(), "resp_native")
			}
			require.NotContains(t, w.Body.String(), "bps_response_failed")
			require.Equal(t, "no", w.Header().Get("X-Accel-Buffering"))
		})
	}
}

func TestNativeUnknownToolDoesNotRegenerateAfterVisibleOutput(t *testing.T) {
	var calls atomic.Int32
	g := gateway(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"Working\"}\n\n")
		writeCorrectionCall(w, "1", "ordinary", `{"name":"undeclared","arguments":{}}`)
	})
	w := httptest.NewRecorder()
	g.ServeHTTP(w, request(correctionRequest))
	require.Equal(t, int32(1), calls.Load())
	require.Contains(t, w.Body.String(), "Working")
	require.Contains(t, w.Body.String(), "basispoints_protocol_error")
}

func TestNativeEncryptedRecoverySameAccountAndBounded(t *testing.T) {
	for _, status := range []int{200, 400, 401, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var bodies [][]byte
			g := gateway(t, func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				bodies = append(bodies, body)
				require.Equal(t, "Bearer "+token(), r.Header.Get("Authorization"))
				if len(bodies) == 1 {
					w.WriteHeader(400)
					fmt.Fprint(w, excelBPSInvalidCiphertext)
					return
				}
				if status != 200 {
					w.WriteHeader(status)
					fmt.Fprint(w, excelBPSInvalidCiphertext)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, completed)
			})
			body := `{"model":"gpt-6-astra","stream":true,"input":[{"role":"user","content":"original task"},{"type":"reasoning","encrypted_content":"opaque"}]}`
			w := httptest.NewRecorder()
			g.ServeHTTP(w, request(body))
			require.Len(t, bodies, 2)
			require.Contains(t, string(bodies[0]), "opaque")
			require.NotContains(t, string(bodies[1]), "opaque")
			require.Contains(t, string(bodies[1]), "original task")
			for _, field := range []string{"model", "metadata", "prompt_cache_key", "reasoning_effort", "context_management"} {
				require.Equal(t, gjson.GetBytes(bodies[0], field).Raw, gjson.GetBytes(bodies[1], field).Raw, field)
			}
			if status == 200 {
				require.Contains(t, w.Body.String(), "response.completed")
			} else {
				require.Equal(t, status, w.Code)
			}
		})
	}
}

func TestNativeEncryptedRecoveryNeverRetriesUnrelatedFailures(t *testing.T) {
	for _, status := range []int{400, 401, 403, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var count atomic.Int32
			g := gateway(t, func(w http.ResponseWriter, r *http.Request) {
				count.Add(1)
				w.WriteHeader(status)
				fmt.Fprint(w, `{"error":{"code":"invalid_value","message":"The encrypted content could not be verified and could not be decrypted or parsed"}}`)
			})
			body := `{"model":"gpt-6-astra","input":[{"role":"user","content":"original task"},{"type":"reasoning","encrypted_content":"opaque"}]}`
			w := httptest.NewRecorder()
			g.ServeHTTP(w, request(body))
			require.Equal(t, int32(1), count.Load())
			require.Equal(t, status, w.Code)
		})
	}
}

func TestNativeFailureLogRetainsClassificationWithoutBody(t *testing.T) {
	g := gateway(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_private\",\"status\":\"failed\",\"output\":[],\"error\":{\"code\":\"basispoints_protocol_error\",\"message\":\"private chat detail\"}}}\n\n")
	})
	capture := new(traceCapture)
	g.SetTraceObserver(capture.add)
	w := httptest.NewRecorder()
	g.ServeHTTP(w, request(simpleRequest))
	events := capture.snapshot()
	require.NotEmpty(t, events)
	require.Contains(t, events[len(events)-1].Message, "response.failed")
	require.Contains(t, events[len(events)-1].Message, "basispoints_protocol_error")
	serialized, err := json.Marshal(events)
	require.NoError(t, err)
	require.NotContains(t, string(serialized), "private")
}
