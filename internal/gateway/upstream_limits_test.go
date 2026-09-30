package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestNativeV294FailureClassificationNoReplay(t *testing.T) {
	for _, tc := range []struct {
		name, payload, code string
		status              int
	}{
		{"flat rate", `{"type":"error","code":"rate_limit_exceeded","message":"PRIVATE"}`, "rate_limit_exceeded", 429},
		{"type rate", `{"type":"response.failed","response":{"error":{"type":"rate_limit_error"}}}`, "basispoints_upstream_error", 429},
		{"quota", `{"type":"error","error":{"code":"insufficient_quota"}}`, "insufficient_quota", 429},
		{"explicit rate", `{"type":"error","status":429,"error":{"code":"invalid_value"}}`, "invalid_value", 429},
		{"explicit non rate", `{"type":"error","status":400,"error":{"code":"rate_limit_exceeded"}}`, "rate_limit_exceeded", 400},
		{"auth", `{"type":"error","error":{"code":"invalid_api_key"}}`, "invalid_api_key", 401},
		{"permission", `{"type":"error","error":{"code":"permission_denied"}}`, "permission_denied", 403},
		{"server", `{"type":"error","error":{"code":"server_error"}}`, "server_error", 500},
		{"cancelled", `{"type":"response.cancelled","response":{"id":"resp_cancelled","status":"cancelled","output":[]}}`, "basispoints_upstream_cancelled", 502},
		{"unknown", `{"type":"error","error":{"code":"PRIVATE_CODE","type":"PRIVATE_TYPE","message":"PRIVATE_BODY"}}`, "basispoints_upstream_error", 502},
	} {
		for _, stream := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, stream), func(t *testing.T) {
				var calls atomic.Int32
				g := gateway(t, func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					w.Header().Set("Content-Type", "text/event-stream")
					w.Header().Set("Retry-After", "30")
					fmt.Fprintf(w, "data: %s\n\n", tc.payload)
					w.(http.Flusher).Flush()
					// 上游终态后仍保持连接，网关应主动结束而不是等到总超时。
					<-r.Context().Done()
				})
				g.requestTimeout = time.Second
				var result Result
				g.report = func(r Result) { result = r }
				var trace traceCapture
				g.SetTraceObserver(trace.add)
				w := httptest.NewRecorder()
				g.ServeHTTP(w, request(strings.Replace(simpleRequest, `"stream":true`, fmt.Sprintf(`"stream":%t`, stream), 1)))
				if stream {
					require.Equal(t, 200, w.Code)
					require.Contains(t, w.Body.String(), tc.code)
				} else {
					require.Equal(t, tc.status, w.Code)
					require.Equal(t, tc.code, gjson.Get(w.Body.String(), "error.code").String())
				}
				require.NotContains(t, w.Body.String(), "PRIVATE")
				require.NotContains(t, w.Body.String(), "response.completed")
				require.EqualValues(t, 1, calls.Load())
				require.False(t, result.Cancelled)
				require.Equal(t, tc.status, result.Status)
				require.Equal(t, tc.code, result.UpstreamCode)
				require.Equal(t, tc.status == 429, g.coolingAccount(t.Context(), "account-1") != nil)
				entries := trace.snapshot()
				last := entries[len(entries)-1]
				require.Equal(t, 200, last.HTTPStatus)
				require.Equal(t, tc.status, last.SemanticStatus)
			})
		}
	}
}

func TestNativeV294SSEFramingAndTerminalBoundary(t *testing.T) {
	for _, tc := range []struct {
		wire    string
		limited bool
	}{
		{"event: error\r\ndata:{\"error\":{\"code\":\"rate_limit_exceeded\"}}\r\n\r\n", true},
		{"data: {\"type\":\"error\",\ndata: \"error\":{\"code\":\"rate_limit_exceeded\"}}\n\n", true},
		{strings.TrimRight(limitWire("error"), "\n"), true},
		{completed + limitWire("error"), false},
		{`data: {"type":"response.output_text.delta","delta":"rate_limit_exceeded"}` + "\n\n" + completed, false},
		{limitWire("response.incomplete"), false},
	} {
		g := gateway(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, tc.wire)
		})
		w := httptest.NewRecorder()
		g.ServeHTTP(w, request(simpleRequest))
		require.Equal(t, tc.limited, g.coolingAccount(t.Context(), "account-1") != nil, tc.wire)
	}
}

func TestLimitHintsAreValidatedAndDoNotInferCooldown(t *testing.T) {
	header := http.Header{}
	header.Set("Retry-After", "30")
	header.Set("X-Ratelimit-Remaining-Requests", "0002")
	header.Set("X-Ratelimit-Remaining-Tokens", "0")
	header.Set("X-Ratelimit-Reset-Requests", "1m30s")
	header.Set("X-Codex-Primary-Used-Percent", "100")
	header.Set("X-Codex-Primary-Reset-After-Seconds", "18000")
	header.Set("X-Codex-Primary-Window-Minutes", "300")
	header.Set("X-Codex-Secondary-Used-Percent", "NaN")
	header.Set("X-Codex-Secondary-Reset-After-Seconds", "PRIVATE")
	header.Set("Authorization", "PRIVATE_CREDENTIAL")
	header.Set("X-Request-Id", "PRIVATE_REQUEST")
	header.Set("X-Ratelimit-Reset-Tokens", "PRIVATE_RESET")
	hints := safeLimitHints(header, time.Now())
	require.Len(t, hints, 7)
	raw, err := json.Marshal(hints)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "PRIVATE")
	require.NotContains(t, string(raw), "NaN")
	var trace traceCapture
	g := gateway(t, func(w http.ResponseWriter, r *http.Request) {
		for name, values := range header {
			w.Header()[name] = values
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, completed)
	})
	g.SetTraceObserver(trace.add)
	w := httptest.NewRecorder()
	g.ServeHTTP(w, request(simpleRequest))
	require.Contains(t, w.Body.String(), "response.completed")
	require.Nil(t, g.coolingAccount(t.Context(), "account-1"), "用量 100% 或提示头本身不能作为 BPS 限流证据")
	entries := trace.snapshot()
	require.Equal(t, hints, entries[len(entries)-1].LimitHints)
}

func TestCooldownMessagesRetainOriginAndRejectExcessiveHeader(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	s := &RateLimits{now: func() time.Time { return now }}
	require.Contains(t, s.window("a", "PRIVATE", true).Error(), "上游未提供有效等待时间")
	s.window("b", "30", true)
	now = now.Add(time.Second)
	local := s.window("b", "", false)
	require.Contains(t, local.Error(), "上游建议不早于")
	require.Equal(t, "29", local.retryAfter)
	require.Equal(t, "7200", s.window("c", "4294967295", true).retryAfter)
	s.window("d", "86400", true)
	require.Equal(t, "86400", s.window("d", "", false).retryAfter, "客户端提示不能因为 2 小时本地调度上限而提早")
}
