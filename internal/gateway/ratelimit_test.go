package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bpslocal/internal/identity"
	"github.com/stretchr/testify/require"
)

func TestBPSCooldownNativeRetryAfterRules(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, header string
		seconds      int
	}{
		{"seconds", "30", 30}, {"date", now.Add(time.Minute).Format(http.TimeFormat), 60},
		{"absent", "", 5}, {"invalid", "private-invalid-header", 5},
		{"negative", "-5", 5}, {"past", now.Add(-time.Minute).Format(http.TimeFormat), 1},
		{"zero", "0", 1}, {"cap", "86400", 7200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &RateLimits{now: func() time.Time { return now }}
			limited := s.window("account-1", tc.header, true)
			require.Equal(t, tc.seconds, limited.seconds)
			require.False(t, limited.local)
			if tc.name == "seconds" || tc.name == "date" || tc.name == "cap" {
				require.Equal(t, tc.header, limited.retryAfter)
			}
			require.NotContains(t, limited.retryAfter, "private")
		})
	}
}

func TestBPSCooldownConcurrentUpdatesNeverShortenAndExpire(t *testing.T) {
	now := time.Now()
	s := &RateLimits{now: func() time.Time { return now }}
	var workers sync.WaitGroup
	for seconds := 1; seconds <= 60; seconds++ {
		workers.Add(1)
		go func(seconds int) { defer workers.Done(); s.window("a", strconv.Itoa(seconds), true) }(seconds)
	}
	workers.Wait()
	require.Equal(t, 60, s.window("a", "1", true).seconds)
	require.Nil(t, s.window("b", "", false))
	now = now.Add(59*time.Second + time.Millisecond)
	require.Equal(t, 1, s.window("a", "", false).seconds)
	now = now.Add(time.Second)
	require.Nil(t, s.window("a", "", false))
	// 本地拒绝不会延长截止时间，停止/启动网关共享状态也不会重置它。
	first, second := New("k", "m", nil, nil), New("k", "m", nil, nil)
	first.SetRateLimits(s)
	second.SetRateLimits(s)
	first.recordRateLimit(context.Background(), "a", "30")
	require.Equal(t, 30, second.coolingAccount(context.Background(), "a").seconds)
}

func TestBPSHTTP429BlocksRetriesThenRecoversAndIsolatesAccounts(t *testing.T) {
	var calls atomic.Int32
	g := gateway(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, "PRIVATE_UPSTREAM "+token())
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, completed)
	})
	now := time.Now()
	g.rateLimits.now = func() time.Time { return now }
	account, err := testAccount()
	require.NoError(t, err)
	g.accountSource = func() (identity.Account, error) { return account, nil }
	var result Result
	g.report = func(r Result) { result = r }
	for attempt := 0; attempt < 6; attempt++ {
		w := httptest.NewRecorder()
		g.ServeHTTP(w, request(simpleRequest))
		require.Equal(t, http.StatusTooManyRequests, w.Code)
		require.Equal(t, "30", w.Header().Get("Retry-After"))
		require.Contains(t, w.Body.String(), typeRateLimited)
		require.NotContains(t, w.Body.String(), "PRIVATE_UPSTREAM")
		require.NotContains(t, w.Body.String(), token())
		require.Equal(t, codeRateLimited, result.Code)
		if attempt > 0 {
			require.Contains(t, result.Message, "本地冷却拦截")
		}
	}
	require.EqualValues(t, 1, calls.Load())
	account.AccessToken = "refreshed-synthetic-token"
	w := httptest.NewRecorder()
	g.ServeHTTP(w, request(simpleRequest))
	require.Equal(t, http.StatusTooManyRequests, w.Code)
	account.AccountID = "account-2"
	w = httptest.NewRecorder()
	g.ServeHTTP(w, request(simpleRequest))
	require.Equal(t, http.StatusOK, w.Code)
	require.EqualValues(t, 2, calls.Load())
	account.AccountID = "account-1"
	now = now.Add(30 * time.Second)
	w = httptest.NewRecorder()
	g.ServeHTTP(w, request(simpleRequest))
	require.Equal(t, http.StatusOK, w.Code)
	require.True(t, result.Success)
	require.EqualValues(t, 3, calls.Load())
}

func limitWire(kind string) string {
	if kind == "error" {
		return "data: {\"type\":\"error\",\"error\":{\"code\":\"rate_limit_exceeded\",\"message\":\"PRIVATE_UPSTREAM\"}}\n\n"
	}
	return fmt.Sprintf("data: {\"type\":%q,\"response\":{\"id\":\"resp_limited\",\"status\":\"failed\",\"output\":[],\"error\":{\"code\":\"rate_limit_exceeded\",\"message\":\"PRIVATE_UPSTREAM\"}}}\n\n", kind)
}

func TestBPSStreamRateLimitPreservesFailureAndBlocksFiveReconnects(t *testing.T) {
	for _, kind := range []string{"error", "response.failed", "response.cancelled"} {
		for _, stream := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/stream=%t", kind, stream), func(t *testing.T) {
				var calls atomic.Int32
				g := gateway(t, func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					w.Header().Set("Content-Type", "text/event-stream")
					w.Header().Set("Retry-After", "60")
					fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial answer\"}\n\n"+limitWire(kind))
				})
				var result Result
				g.report = func(r Result) { result = r }
				var trace traceCapture
				g.SetTraceObserver(trace.add)
				body := strings.Replace(simpleRequest, "\"stream\":true", fmt.Sprintf("\"stream\":%t", stream), 1)
				w := httptest.NewRecorder()
				g.ServeHTTP(w, request(body))
				if stream {
					require.Equal(t, http.StatusOK, w.Code)
					require.Contains(t, w.Body.String(), "partial answer")
					require.Contains(t, w.Body.String(), codeUpstreamRateLimited)
				} else {
					require.Equal(t, http.StatusTooManyRequests, w.Code)
				}
				require.NotContains(t, w.Body.String(), "response.completed")
				require.Equal(t, codeRateLimited, result.Code)
				require.False(t, result.Success)
				events := trace.snapshot()
				last := events[len(events)-1]
				require.Equal(t, http.StatusOK, last.HTTPStatus)
				require.Contains(t, last.Message, "上游限流")
				require.NotContains(t, last.Message, "PRIVATE_UPSTREAM")
				for retry := 0; retry < 5; retry++ {
					w = httptest.NewRecorder()
					g.ServeHTTP(w, request(body))
					require.Equal(t, http.StatusTooManyRequests, w.Code)
					require.NotEmpty(t, w.Header().Get("Retry-After"))
				}
				require.EqualValues(t, 1, calls.Load())
				events = trace.snapshot()
				last = events[len(events)-1]
				require.Zero(t, last.Attempt)
				require.Zero(t, last.HTTPStatus)
				require.Contains(t, last.Message, "本地冷却拦截")
			})
		}
	}
}

func TestBPSUpload429PreventsResponsesAndFurtherUploads(t *testing.T) {
	data, _ := inlinePNG(t)
	var uploads, responses atomic.Int32
	g := imageGateway(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/basispoints/api/attachments" {
			responses.Add(1)
			return
		}
		uploads.Add(1)
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	for attempt := 0; attempt < 2; attempt++ {
		w := httptest.NewRecorder()
		g.ServeHTTP(w, request(imageRequest(t, imagePart(data))))
		require.Equal(t, http.StatusTooManyRequests, w.Code)
		require.Contains(t, w.Body.String(), codeRateLimited)
	}
	require.EqualValues(t, 1, uploads.Load())
	require.Zero(t, responses.Load())
}

func TestBPSCorrectionRateLimitStopsRepairAndPreservesFailure(t *testing.T) {
	for _, streamFailure := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", streamFailure), func(t *testing.T) {
			var calls atomic.Int32
			g := gateway(t, func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					w.Header().Set("Retry-After", "900")
					writeCorrectionCall(w, "1", "missing marker", correctionPayload)
					return
				}
				w.Header().Set("Retry-After", "30")
				if streamFailure {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, limitWire("error"))
				} else {
					w.WriteHeader(http.StatusTooManyRequests)
				}
			})
			var result Result
			g.report = func(r Result) { result = r }
			w := httptest.NewRecorder()
			g.ServeHTTP(w, request(correctionRequest))
			require.NotContains(t, w.Body.String(), "response.completed")
			require.Equal(t, codeRateLimited, result.Code)
			require.EqualValues(t, 2, calls.Load())
			w = httptest.NewRecorder()
			g.ServeHTTP(w, request(correctionRequest))
			require.Equal(t, http.StatusTooManyRequests, w.Code)
			require.Equal(t, "30", w.Header().Get("Retry-After"), "纠错限流应使用纠错响应的等待时间")
			require.EqualValues(t, 2, calls.Load())
		})
	}
}
