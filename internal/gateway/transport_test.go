package gateway

import (
	"bpslocal/internal/transportdiag"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"github.com/stretchr/testify/require"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"testing"
	"time"
)

type bpsPolicyConn struct {
	net.Conn
	proto string
}

func (c bpsPolicyConn) ConnectionState() tls.ConnectionState {
	return tls.ConnectionState{NegotiatedProtocol: c.proto}
}
func bpsPolicyTrace(proto string) *transportdiag.Trace {
	x := &transportdiag.Trace{}
	r, _ := http.NewRequest("POST", "https://example.test", nil)
	r = x.Request(r)
	tr := httptrace.ContextClientTrace(r.Context())
	tr.GetConn("example.test")
	if proto != "" {
		tr.GotConn(httptrace.GotConnInfo{Conn: bpsPolicyConn{proto: proto}})
	}
	return x
}
func TestBPSFallbackBoundariesAndExpiry(t *testing.T) {
	proxy := "http://private:credential@127.0.0.1:19178"
	for _, tc := range []struct {
		name, proto, proxy string
		err                error
		cancel, expected   bool
	}{
		{"h2_eof", "h2", proxy, io.ErrUnexpectedEOF, false, true},
		{"tls_eof", "", proxy, io.ErrUnexpectedEOF, false, false},
		{"h1_eof", "http/1.1", proxy, io.ErrUnexpectedEOF, false, false},
		{"direct", "h2", "", io.ErrUnexpectedEOF, false, false},
		{"cancelled", "h2", proxy, io.ErrUnexpectedEOF, true, false},
		{"deadline", "h2", proxy, context.DeadlineExceeded, false, false},
		{"application", "h2", proxy, errors.New("rejected"), false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newBPSTransport(http.DefaultTransport.(*http.Transport).Clone())
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			svc.recordFailure(ctx, tc.proxy, bpsPolicyTrace(tc.proto), tc.err)
			require.Equal(t, tc.expected, svc.http1Active(tc.proxy, time.Now()))
			require.False(t, svc.http1Active("http://127.0.0.1:19000", time.Now()))
			if tc.expected {
				key := sha256.Sum256([]byte(tc.proxy))
				svc.mu.Lock()
				svc.fallbacks[key] = bpsHTTP2Fallback{expiresAt: time.Now().Add(-time.Second), started: true}
				svc.mu.Unlock()
				require.False(t, svc.http1Active(tc.proxy, time.Now()))
			}
		})
	}
}

type bpsErrorBody struct{ err error }

func (b bpsErrorBody) Read([]byte) (int, error) { return 0, b.err }
func (b bpsErrorBody) Close() error             { return nil }
func TestBPSFeedbackBodyDoesNotTreatNormalEOFAsFailure(t *testing.T) {
	calls := 0
	b := &bpsFeedbackBody{ReadCloser: io.NopCloser(strings.NewReader("done")), failed: func(error) { calls++ }}
	_, err := io.ReadAll(b)
	require.NoError(t, err)
	require.Zero(t, calls)
	require.NoError(t, b.Close())
	b = &bpsFeedbackBody{ReadCloser: bpsErrorBody{io.ErrUnexpectedEOF}, failed: func(error) { calls++ }}
	_, _ = b.Read(make([]byte, 1))
	_, _ = b.Read(make([]byte, 1))
	require.Equal(t, 1, calls)
	require.NoError(t, b.Close())
}

func TestBPSFallbackWaitsForNodeCooldown(t *testing.T) {
	svc := newBPSTransport(http.DefaultTransport.(*http.Transport).Clone())
	proxy := "http://127.0.0.1:19178"
	now := time.Now()
	svc.recordFailure(t.Context(), proxy, bpsPolicyTrace("h2"), io.ErrUnexpectedEOF)
	// This is beyond the longest node cooldown, but within pending retention.
	reused := now.Add(31 * time.Minute)
	require.True(t, svc.http1Active(proxy, reused))
	require.True(t, svc.http1Active(proxy, reused.Add(59*time.Second)))
	require.False(t, svc.http1Active(proxy, reused.Add(time.Minute)))
	svc.recordFailure(t.Context(), proxy, bpsPolicyTrace("h2"), io.ErrUnexpectedEOF)
	require.False(t, svc.http1Active(proxy, now.Add(2*time.Hour)), "unused pending state expires")
}

func TestBPSLocalProxyPrefersHTTP1(t *testing.T) {
	for _, tc := range []struct {
		proxy string
		want  bool
	}{
		{proxy: "http://127.0.0.1:6789", want: true},
		{proxy: "http://localhost:6789", want: true},
		{proxy: "http://[::1]:6789", want: true},
		{proxy: "http://proxy.example:8080", want: false},
		{proxy: "", want: false},
	} {
		require.Equal(t, tc.want, preferHTTP1Proxy(tc.proxy), tc.proxy)
	}
}

func TestBPSProductionTransportKeepsNativeHealthSettings(t *testing.T) {
	g := New("local-key", "gpt-6-astra", nil, nil)
	transport, ok := g.client.Transport.(*bpsTransport)
	require.True(t, ok)
	require.Equal(t, 300*time.Second, transport.h2.ResponseHeaderTimeout)
	require.True(t, transport.h2.ForceAttemptHTTP2)
	require.Equal(t, 10*time.Second, transport.h2.HTTP2.SendPingTimeout)
	require.Equal(t, 5*time.Second, transport.h2.HTTP2.PingTimeout)
	require.True(t, transport.h1.Protocols.HTTP1())
	require.False(t, transport.h1.Protocols.HTTP2())
	transport.CloseIdleConnections()
}
