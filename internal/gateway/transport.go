package gateway

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"bpslocal/internal/transportdiag"
)

const bpsHTTP2FallbackTTL = time.Minute
const bpsHTTP2FallbackMaxIdle = time.Hour
const maxBPSFallbackEntries = 256

type bpsHTTP2Fallback struct {
	expiresAt time.Time
	started   bool
}
type bpsTransport struct {
	h2, h1    *http.Transport
	mu        sync.Mutex
	fallbacks map[[32]byte]bpsHTTP2Fallback
}

func newBPSTransport(h2 *http.Transport) *bpsTransport {
	h1 := h2.Clone()
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	h1.Protocols = protocols
	h1.ForceAttemptHTTP2 = false
	return &bpsTransport{h2: h2, h1: h1, fallbacks: make(map[[32]byte]bpsHTTP2Fallback)}
}
func (t *bpsTransport) CloseIdleConnections() {
	t.h2.CloseIdleConnections()
	t.h1.CloseIdleConnections()
}

// 本机代理默认走 H1，避免常见本地代理重置 BPS 的 H2 长连接；远端代理
// 仍沿用 H2 故障后短时试用 H1 的策略；绝不重发已经发送的失败请求。
func (t *bpsTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	proxyKey := ""
	if t.h2.Proxy != nil {
		proxy, err := t.h2.Proxy(request)
		if err != nil {
			return nil, err
		}
		if proxy != nil && (proxy.Scheme == "http" || proxy.Scheme == "https") {
			proxyKey = proxy.String()
		}
	}
	selected := t.h2
	preferH1 := proxyKey != "" && preferHTTP1Proxy(proxyKey)
	if proxyKey != "" && (preferH1 || t.http1Active(proxyKey, time.Now())) {
		selected = t.h1
	}
	trace := new(transportdiag.Trace)
	request = trace.Request(request)
	if preferH1 {
		if requestTrace := traceFrom(request.Context()); requestTrace != nil {
			requestTrace.stage(traceConnect, "检测到本机代理；优先使用 HTTP/1.1 连接 BPS")
		}
	}
	response, err := selected.RoundTrip(request)
	t.recordFailure(request.Context(), proxyKey, trace, err)
	if response != nil && response.Body != nil {
		response.Body = &bpsFeedbackBody{ReadCloser: response.Body, failed: func(err error) { t.recordFailure(request.Context(), proxyKey, trace, err) }}
	}
	return response, err
}

func preferHTTP1Proxy(proxy string) bool {
	parsed, err := url.Parse(proxy)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}
func (t *bpsTransport) http1Active(proxy string, now time.Time) bool {
	key := sha256.Sum256([]byte(proxy))
	t.mu.Lock()
	defer t.mu.Unlock()
	state, ok := t.fallbacks[key]
	if ok && !now.Before(state.expiresAt) {
		delete(t.fallbacks, key)
		return false
	}
	if ok && !state.started {
		state.started = true
		state.expiresAt = now.Add(bpsHTTP2FallbackTTL)
		t.fallbacks[key] = state
	}
	return ok
}
func (t *bpsTransport) recordFailure(ctx context.Context, proxy string, trace *transportdiag.Trace, err error) {
	if proxy == "" || !trace.NegotiatedHTTP2() || ctx.Err() != nil {
		return
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	kind := transportdiag.Classify(err)
	if kind != "unexpected_eof" && kind != "connection_reset" && kind != "http2_error" {
		return
	}
	key := sha256.Sum256([]byte(proxy))
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	if state := t.fallbacks[key]; now.Before(state.expiresAt) {
		return
	}
	for key, state := range t.fallbacks {
		if !now.Before(state.expiresAt) {
			delete(t.fallbacks, key)
		}
	}
	if len(t.fallbacks) >= maxBPSFallbackEntries {
		return
	}
	t.fallbacks[key] = bpsHTTP2Fallback{expiresAt: now.Add(bpsHTTP2FallbackMaxIdle)}
	traceFrom(ctx).stage(traceStreaming, "检测到 HTTP/2 代理连接故障；本次不重放，后续请求临时使用 HTTP/1.1")
}

type bpsFeedbackBody struct {
	io.ReadCloser
	once   sync.Once
	failed func(error)
}

func (b *bpsFeedbackBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		b.once.Do(func() { b.failed(err) })
	}
	return n, err
}
