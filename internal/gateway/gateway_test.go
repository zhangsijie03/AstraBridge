package gateway

import (
	"bpslocal/internal/identity"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const KeyHeader = "X-BPS-Local-Key"          // 旧客户端自定义头不再用于鉴权。
func testAccount() (identity.Account, error) { return identity.FromToken(token(), "account-1") }
func token() string {
	return "header." + base64.RawURLEncoding.EncodeToString([]byte(`{"exp":4102444800,"https://api.openai.com/auth":{"chatgpt_account_id":"account-1"}}`)) + ".sig"
}
func request(body string) *http.Request {
	r := httptest.NewRequest("POST", "http://127.0.0.1:17861/v1/responses", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(KeyHeader, "local-key")
	r.Header.Set("Authorization", "Bearer local-key")
	r.Header.Set("Chatgpt-Account-Id", "account-1")
	r.Header.Set("thread-id", "thread-1")
	return r
}

const simpleRequest = `{"model":"gpt-6-astra","input":"hello","stream":true,"reasoning":{"effort":"ultra"}}`
const completed = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"model\":\"gpt-6-astra\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}]}}\n\n"

func gateway(t *testing.T, h http.HandlerFunc) *Gateway {
	t.Helper()
	u := httptest.NewServer(h)
	t.Cleanup(u.Close)
	g := New("local-key", "gpt-6-astra", testAccount, nil)
	g.endpoint = u.URL
	g.client = u.Client()
	return g
}

func TestDefaultRequestTimeoutAllowsLongTurns(t *testing.T) {
	g := New("local-key", "gpt-6-astra", testAccount, nil)
	if g.requestTimeout != time.Hour {
		t.Fatalf("unexpected default request timeout: %s", g.requestTimeout)
	}
}

func TestRejectsUnauthorizedBeforeUpstream(t *testing.T) {
	var called atomic.Int32
	g := gateway(t, func(w http.ResponseWriter, r *http.Request) { called.Add(1) })
	for _, kind := range []string{"key", "origin", "host", "bearer"} {
		r := request(simpleRequest)
		switch kind {
		case "key":
			r.Header.Set("Authorization", "Bearer wrong-key")
		case "origin":
			r.Header.Set("Origin", "https://evil.test")
		case "host":
			r.Host = "evil.test"
		case "bearer":
			r.Header.Del("Authorization")

		}
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r)
		if w.Code < 400 {
			t.Fatalf("%s allowed", kind)
		}
	}
	if called.Load() != 0 {
		t.Fatal("unauthorized request reached upstream")
	}
}
func TestStreamingAndWireFormat(t *testing.T) {
	g := gateway(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token() || r.Header.Get("X-Basispoints-Auth-Mode") != "chatgpt" {
			t.Error("incorrect upstream auth")
		}
		if r.Header.Get(KeyHeader) != "" {
			t.Error("local key leaked upstream")
		}
		var data map[string]interface{}
		if e := json.NewDecoder(r.Body).Decode(&data); e != nil {
			t.Error(e)
		}
		if data["model"] != "gpt-6-astra" || data["reasoning_effort"] != "xhigh" {
			t.Error("model or effort changed incorrectly")
		}
		management, ok := data["context_management"].([]interface{})
		if !ok || len(management) != 1 || management[0].(map[string]interface{})["compact_threshold"] != float64(920000) {
			t.Errorf("unexpected compaction threshold: %#v", data["context_management"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\n"+completed)
	})
	w := httptest.NewRecorder()
	g.ServeHTTP(w, request(simpleRequest))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "response.completed") || !strings.Contains(w.Body.String(), `"effort":"xhigh"`) {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestUpstreamErrorsNeverEchoSecrets(t *testing.T) {
	g := gateway(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		fmt.Fprint(w, `{"error":{"message":"SECRET-PROMPT `+token()+`"}}`)
	})
	w := httptest.NewRecorder()
	g.ServeHTTP(w, request(simpleRequest))
	if w.Code != 403 || strings.Contains(w.Body.String(), "SECRET-PROMPT") || strings.Contains(w.Body.String(), token()) {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestUpstreamValidationKeepsSafeClassification(t *testing.T) {
	g := gateway(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		fmt.Fprint(w, `{"error":{"type":"invalid_request_error","code":"tool_schema_invalid","param":"input[3].arguments","message":"secret prompt"}}`)
	})
	var result Result
	g.report = func(r Result) { result = r }
	w := httptest.NewRecorder()
	g.ServeHTTP(w, request(simpleRequest))
	if w.Code != http.StatusUnprocessableEntity || result.UpstreamType != "invalid_request_error" || result.UpstreamCode != "tool_schema_invalid" || len(result.UpstreamFields) != 1 || result.UpstreamFields[0] != "input[3].arguments" {
		t.Fatalf("missing safe upstream classification: %+v; body=%s", result, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "secret prompt") {
		t.Fatal("upstream message leaked to client")
	}
}
func TestStreamTruncationEmitsOneFailure(t *testing.T) {
	g := gateway(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")
	})
	w := httptest.NewRecorder()
	g.ServeHTTP(w, request(simpleRequest))
	if strings.Count(w.Body.String(), "event: response.failed") != 1 {
		t.Fatal(w.Body.String())
	}
}
func TestClientCancellationStopsUpstream(t *testing.T) {
	cancelled := make(chan struct{})
	started := make(chan struct{})
	g := gateway(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
		close(cancelled)
	})
	var result Result
	g.report = func(r Result) { result = r }
	ctx, cancel := context.WithCancel(context.Background())
	r := request(simpleRequest).WithContext(ctx)
	done := make(chan struct{})
	go func() { g.ServeHTTP(httptest.NewRecorder(), r); close(done) }()
	<-started
	cancel()
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream not cancelled")
	}
	<-done
	if result.Code != "client_cancelled" || result.Success {
		t.Fatalf("cancel misclassified: %+v", result)
	}
}
func TestNonstreamReturnsResponseJSON(t *testing.T) {
	g := gateway(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, completed)
	})
	w := httptest.NewRecorder()
	g.ServeHTTP(w, request(`{"model":"gpt-6-astra","input":"hello","stream":false}`))
	var v map[string]interface{}
	if json.Unmarshal(w.Body.Bytes(), &v) != nil || v["id"] != "resp_1" {
		t.Fatal(w.Body.String())
	}
}
func TestRedirectDoesNotForwardCredentials(t *testing.T) {
	var calls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	g := New("local-key", "gpt-6-astra", testAccount, nil)
	g.endpoint = source.URL
	w := httptest.NewRecorder()
	g.ServeHTTP(w, request(simpleRequest))
	if calls.Load() != 0 || w.Code < 400 {
		t.Fatal("followed upstream redirect")
	}
}
func TestCompactAddsTrigger(t *testing.T) {
	g := gateway(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(b), "compaction_trigger") {
			t.Error("missing compact trigger")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, completed)
	})
	r := request(`{"model":"gpt-6-astra","input":"hello"}`)
	r.URL.Path = "/v1/responses/compact"
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestMissingThreadIdentityCannotShareReplayScope(t *testing.T) {
	r1 := request(simpleRequest)
	r1.Header.Del("thread-id")
	r1.Header.Del("session_id")
	r2 := request(simpleRequest)
	r2.Header.Del("thread-id")
	r2.Header.Del("session_id")
	var source map[string]json.RawMessage
	_ = json.Unmarshal([]byte(simpleRequest), &source)
	if requestScope(r1, source, "account-1") != "" || requestScope(r2, source, "account-1") != "" {
		t.Fatal("unidentified requests must not share a replay cache")
	}
}
func TestThreadAndAccountScopeSeparation(t *testing.T) {
	r1 := request(simpleRequest)
	r2 := request(simpleRequest)
	r2.Header.Set("thread-id", "thread-2")
	var source map[string]json.RawMessage
	_ = json.Unmarshal([]byte(simpleRequest), &source)
	if requestScope(r1, source, "a") == requestScope(r2, source, "a") || requestScope(r1, source, "a") == requestScope(r1, source, "b") {
		t.Fatal("thread or account cache scope collision")
	}
	if requestScope(r1, source, "a") != requestScope(r1, source, "a") {
		t.Fatal("explicit thread must retain continuation")
	}
}

// A waiting model must keep the downstream connection alive without fake model output.
func TestWaitingStreamSendsHeartbeat(t *testing.T) {
	g := gateway(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	local := httptest.NewServer(g)
	defer local.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	r := request(simpleRequest).WithContext(ctx)
	r.URL.Scheme = "http"
	r.URL.Host = strings.TrimPrefix(local.URL, "http://")
	r.RequestURI = ""
	resp, err := local.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 128)
	n, err := resp.Body.Read(buf)
	if err != nil || !strings.Contains(string(buf[:n]), ": keepalive") {
		t.Fatalf("no initial heartbeat: %q %v", buf[:n], err)
	}
}

func TestCancelDuringWaitingStreamIsNeutral(t *testing.T) {
	started := make(chan struct{})
	g := gateway(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	results := make(chan Result, 1)
	g.report = func(r Result) { results <- r }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &notifyingWriter{ResponseRecorder: httptest.NewRecorder(), flushed: started}
	done := make(chan struct{})
	go func() { g.ServeHTTP(w, request(simpleRequest).WithContext(ctx)); close(done) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("stream never started")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handler did not stop")
	}
	result := <-results
	if !result.Cancelled || result.Success || result.Code != codeCancelled || result.Status != 0 {
		t.Fatalf("cancel became failure: %+v", result)
	}
	if strings.Contains(w.Body.String(), "response.failed") {
		t.Fatal("emitted model failure for client stop")
	}
}

type notifyingWriter struct {
	*httptest.ResponseRecorder
	flushed chan struct{}
	once    sync.Once
}

func (w *notifyingWriter) Flush() { w.ResponseRecorder.Flush(); w.once.Do(func() { close(w.flushed) }) }

func TestWaitingHeartbeatRepeatsAndTotalDeadlineStillApplies(t *testing.T) {
	g := gateway(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	g.heartbeatInterval = 10 * time.Millisecond
	g.requestTimeout = 100 * time.Millisecond
	var result Result
	g.report = func(r Result) { result = r }
	w := httptest.NewRecorder()
	g.ServeHTTP(w, request(simpleRequest))
	if strings.Count(w.Body.String(), ": keepalive") < 2 {
		t.Fatal("missing repeated heartbeats", w.Body.String())
	}
	if result.Cancelled || result.Code != codeTimeout || result.Success {
		t.Fatalf("deadline misclassified: %+v", result)
	}
	if strings.Count(w.Body.String(), "event: response.failed") != 1 {
		t.Fatal("missing or duplicate terminal failure", w.Body.String())
	}
}

func TestUnsupportedSchemaReportsFormatNotAccount(t *testing.T) {
	g := gateway(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unsupported schema reached upstream") })
	var result Result
	g.report = func(r Result) { result = r }
	w := httptest.NewRecorder()
	g.ServeHTTP(w, request(`{"model":"gpt-6-astra","input":"x","text":{"format":{"type":"json_schema","schema":{"type":"array"}}}}`))
	if w.Code != 400 || !strings.Contains(result.Message, "结构化格式") {
		t.Fatalf("wrong explanation: %+v", result)
	}
}

// 写入缓冲成功不代表客户端收到数据；刷新失败不能计作成功请求。
func TestFlushFailureIsNotSuccessfulRequest(t *testing.T) {
	g := gateway(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, completed)
	})
	var result Result
	g.report = func(r Result) { result = r }
	w := &failingFlushWriter{ResponseRecorder: httptest.NewRecorder()}
	g.ServeHTTP(w, request(simpleRequest))
	if result.Success || !result.Cancelled {
		t.Fatalf("flush failure recorded as success: %+v", result)
	}
}

type failingFlushWriter struct{ *httptest.ResponseRecorder }

func (w *failingFlushWriter) FlushError() error { return io.ErrClosedPipe }
