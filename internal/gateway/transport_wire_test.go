package gateway

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 使用真实 CONNECT 隧道和 TLS ALPN，而非只检查配置字段，防止克隆的 H2 配置
// 让名义上的 H1 transport 实际协商 H2，再将二进制帧交给 H1 解析器。
func TestBPSHTTP1ProxyNegotiatesHTTP1OnWire(t *testing.T) {
	for _, warmed := range []bool{false, true} {
		t.Run(fmt.Sprintf("initialized_parent_%t", warmed), func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				fmt.Fprintf(w, "%s/%s", r.Proto, r.TLS.NegotiatedProtocol)
			}))
			upstream.EnableHTTP2 = true
			upstream.TLS = &tls.Config{NextProtos: []string{"h2", "http/1.1"}}
			upstream.StartTLS()
			defer upstream.Close()
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodConnect || r.Host != upstream.Listener.Addr().String() {
					http.Error(w, "unexpected tunnel", http.StatusBadRequest)
					return
				}
				remote, err := net.DialTimeout("tcp", r.Host, time.Second)
				if err != nil {
					http.Error(w, "dial failed", http.StatusBadGateway)
					return
				}
				defer remote.Close()
				local, buffered, err := w.(http.Hijacker).Hijack()
				if err != nil {
					return
				}
				defer local.Close()
				_ = local.SetDeadline(time.Now().Add(5 * time.Second))
				_ = remote.SetDeadline(time.Now().Add(5 * time.Second))
				_, _ = buffered.WriteString("HTTP/1.1 200 Connection established\r\n\r\n")
				_ = buffered.Flush()
				done := make(chan struct{})
				go func() { _, _ = io.Copy(remote, buffered); close(done) }()
				_, _ = io.Copy(local, remote)
				_ = local.Close()
				<-done
			}))
			defer proxy.Close()
			proxyURL, err := url.Parse(proxy.URL)
			require.NoError(t, err)
			roots := x509.NewCertPool()
			roots.AddCert(upstream.Certificate())
			parent := http.DefaultTransport.(*http.Transport).Clone()
			parent.Proxy = http.ProxyURL(proxyURL)
			parent.TLSClientConfig = &tls.Config{RootCAs: roots}
			parent.ForceAttemptHTTP2 = true
			if warmed {
				response, err := (&http.Client{Transport: parent}).Get(upstream.URL)
				require.NoError(t, err)
				_, _ = io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()
				require.Equal(t, 2, response.ProtoMajor)
			}
			transport := newBPSTransport(parent)
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
			for attempt := 0; attempt < 2; attempt++ {
				response, err := client.Post(upstream.URL, "application/json", nil)
				require.NoError(t, err)
				body, err := io.ReadAll(response.Body)
				_ = response.Body.Close()
				require.NoError(t, err)
				require.Equal(t, "HTTP/1.1/http/1.1", string(body))
				require.Equal(t, 1, response.ProtoMajor)
			}
			wantCalls := int32(2)
			if warmed {
				wantCalls++
			}
			require.Equal(t, wantCalls, calls.Load(), "POST must not be replayed")
			// H1 的 TLS 配置必须独立，不能破坏直连 H2 的能力。
			parent.Proxy = nil
			response, err := client.Get(upstream.URL)
			require.NoError(t, err)
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			require.Equal(t, 2, response.ProtoMajor)
		})
	}
}
