package gateway

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type roundTripError func(*http.Request) (*http.Response, error)

func (f roundTripError) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestUpstreamBackoffExponentialAndReset(t *testing.T) {
	now := time.Date(2026, 10, 1, 14, 0, 0, 0, time.UTC)
	b := newUpstreamBackoff()
	b.now = func() time.Time { return now }

	require.Equal(t, 2*time.Second, b.failure())
	require.Equal(t, 2*time.Second, b.remaining())
	now = now.Add(2 * time.Second)
	require.Equal(t, 4*time.Second, b.failure())
	now = now.Add(4 * time.Second)
	require.Equal(t, 8*time.Second, b.failure())
	now = now.Add(8 * time.Second)
	require.Equal(t, 16*time.Second, b.failure())
	now = now.Add(16 * time.Second)
	require.Equal(t, 30*time.Second, b.failure())

	b.success()
	require.Zero(t, b.remaining())
	require.Equal(t, 2*time.Second, b.failure())
}

func TestBackoffSecondsRoundsUp(t *testing.T) {
	require.Equal(t, 0, backoffSeconds(0))
	require.Equal(t, 1, backoffSeconds(1))
	require.Equal(t, 2, backoffSeconds(time.Second+time.Nanosecond))
}

func TestServeHTTPBacksOffAfterUpstreamConnectionFailure(t *testing.T) {
	var calls int
	g := New("local-key", "gpt-6-astra", testAccount, nil)
	g.endpoint = "https://example.test/v1/responses"
	g.client = &http.Client{Transport: roundTripError(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("proxy connection reset")
	})}

	first := httptest.NewRecorder()
	g.ServeHTTP(first, request(simpleRequest))
	require.Equal(t, http.StatusBadGateway, first.Code)
	require.Equal(t, "2", first.Header().Get("Retry-After"))
	require.Contains(t, first.Body.String(), "建议 2 秒后重试")

	second := httptest.NewRecorder()
	g.ServeHTTP(second, request(simpleRequest))
	require.Equal(t, http.StatusServiceUnavailable, second.Code)
	require.Equal(t, "2", second.Header().Get("Retry-After"))
	require.Contains(t, second.Body.String(), codeBackoff)
	require.Equal(t, 1, calls)
}
