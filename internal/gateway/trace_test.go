package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTransportFailureLogsEvidenceWithoutRawError(t *testing.T) {
	var capture traceCapture
	g := New("local-secret", "gpt-6-astra", nil, nil)
	g.SetTraceObserver(capture.add)
	ctx, trace := g.beginTrace(context.Background())
	trace.transportFailure(bpsPolicyTrace("h2"), errors.New("malformed HTTP response SECRET-PROMPT http://user:password@proxy.test"))
	trace.result(Result{Code: codeConnection, Status: 502})
	trace.finish(ctx, false)
	events := capture.snapshot()
	last := events[len(events)-1]
	for _, evidence := range []string{"transport_error", "协议 h2", "阶段 connection_ready"} {
		if !strings.Contains(last.Message, evidence) {
			t.Fatalf("missing %s: %+v", evidence, last)
		}
	}
	raw, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"SECRET-PROMPT", "password", "proxy.test", "local-secret"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("diagnostics leaked sensitive error text")
		}
	}
}

type traceCapture struct {
	mu     sync.Mutex
	events []TraceEvent
}

func (c *traceCapture) add(e TraceEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
}
func (c *traceCapture) snapshot() []TraceEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]TraceEvent(nil), c.events...)
}

func TestTraceDistinguishesUpstreamBytesFromClientHeartbeat(t *testing.T) {
	var capture traceCapture
	g := New("", "", nil, nil)
	g.SetTraceObserver(capture.add)
	ctx, tr := g.beginTrace(context.Background())
	tr.attempt(false)
	tr.headers(200)
	// 上游曾返回数据后长时间无新字节，本地心跳不能刷新上游活动时间。
	body := traceBody(ctx, io.NopCloser(strings.NewReader("sensitive response body")))
	_, _ = io.ReadAll(body)
	_ = body.Close()
	tr.mu.Lock()
	tr.lastByte = time.Now().Add(-40 * time.Second)
	tr.mu.Unlock()
	tr.publish()
	r := request(simpleRequest)
	g.heartbeatInterval = time.Millisecond
	forwardCtx, cancel := context.WithTimeout(ctx, 15*time.Millisecond)
	defer cancel()
	pipe, writer := io.Pipe()
	defer pipe.Close()
	defer writer.Close()
	result := Result{}
	g.forwardStream(httptest.NewRecorder(), r, forwardCtx, pipe, true, &result, nil)
	tr.publish()
	tr.finish(ctx, false)
	events := capture.snapshot()
	e := events[len(events)-2]
	if e.QuietMS < 40000 || e.ClientEvents != 0 || e.UpstreamBytes != int64(len("sensitive response body")) {
		t.Fatalf("heartbeat counted as progress: %+v", e)
	}
	for _, e := range events {
		if strings.Contains(e.Message, "sensitive") {
			t.Fatal("response text leaked")
		}
	}
}

func TestTraceNativeRepairMetadataAndRedaction(t *testing.T) {
	var capture traceCapture
	calls := 0
	g := gateway(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			writeCorrectionCall(w, "1", "missing marker", correctionPayload)
		} else {
			writeCorrectionCall(w, "2", "codex2api.custom/exec", correctionPayload)
		}
	})
	g.SetTraceObserver(capture.add)
	w := httptest.NewRecorder()
	g.ServeHTTP(w, request(correctionRequest))
	events := capture.snapshot()
	if len(events) == 0 {
		t.Fatal("no diagnostics")
	}
	last := events[len(events)-1]
	if last.Stage != traceCompleted || last.Attempt != 2 || last.ToolCalls != 1 || last.UpstreamBytes == 0 || last.ClientEvents == 0 {
		t.Fatalf("incorrect completion trace: %+v", last)
	}
	repair := false
	for _, e := range events {
		if e.RequestID != last.RequestID {
			t.Fatal("request ID changed")
		}
		if e.Stage == traceRepair {
			repair = true
		}
	}
	if !repair {
		t.Fatal("missing correction stage")
	}
	data, _ := json.Marshal(events)
	for _, secret := range []string{token(), "local-key", correctionPayload, "account-1", "edit a file", "run_officejs"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("diagnostics leaked %q", secret)
		}
	}
	before := len(events)
	time.Sleep(15 * time.Millisecond)
	if len(capture.snapshot()) != before {
		t.Fatal("diagnostics continued after completion")
	}
}

func TestTraceConcurrentRequestsHaveSeparateLifecycles(t *testing.T) {
	var capture traceCapture
	g := gateway(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, completed)
	})
	g.SetTraceObserver(capture.add)
	var tasks sync.WaitGroup
	for i := 0; i < 6; i++ {
		tasks.Go(func() { g.ServeHTTP(httptest.NewRecorder(), request(simpleRequest)) })
	}
	tasks.Wait()
	finals := map[string]int{}
	for _, e := range capture.snapshot() {
		if e.Stage == traceCompleted {
			finals[e.RequestID]++
		}
	}
	if len(finals) != 6 {
		t.Fatalf("requests merged: %+v", finals)
	}
	for _, count := range finals {
		if count != 1 {
			t.Fatal("duplicate final event")
		}
	}
}

func TestTraceCancelledRequestStopsSnapshots(t *testing.T) {
	var capture traceCapture
	g := New("", "", nil, nil)
	g.SetTraceObserver(capture.add)
	ctx, cancel := context.WithCancel(context.Background())
	ctx, tr := g.beginTrace(ctx)
	cancel()
	tr.finish(ctx, false)
	events := capture.snapshot()
	if events[len(events)-1].Stage != traceCancelled {
		t.Fatal("cancellation not recorded")
	}
}

// 静默等待时必须仍有诊断快照，取消后 ticker 必须退出，避免“日志自己也卡住”。
func TestTraceEmitsPeriodicSnapshotWhileUpstreamIsSilent(t *testing.T) {
	snapshots := make(chan TraceEvent, 16)
	g := New("", "", nil, nil)
	g.SetTraceObserver(func(e TraceEvent) { snapshots <- e })
	ctx, tr := g.beginTrace(context.Background())
	tr.attempt(false)
	<-snapshots
	<-snapshots
	select {
	case e := <-snapshots:
		if e.Stage != traceConnect || e.QuietMS < 4500 || e.UpstreamBytes != 0 {
			t.Fatalf("invalid waiting snapshot: %+v", e)
		}
	case <-time.After(7 * time.Second):
		t.Fatal("no periodic waiting snapshot")
	}
	tr.finish(ctx, false)
}

func TestTraceIgnoresLateUpstreamDataAfterCancellation(t *testing.T) {
	var capture traceCapture
	g := New("", "", nil, nil)
	g.SetTraceObserver(capture.add)
	ctx, tr := g.beginTrace(context.Background())
	body := traceBody(ctx, io.NopCloser(strings.NewReader("late private chunk")))
	tr.finish(ctx, false)
	count := len(capture.snapshot())
	_, _ = io.ReadAll(body)
	_ = body.Close()
	tr.publish()
	tr.headers(200)
	tr.attempt(true)
	tr.forwarded(true, true)
	if len(capture.snapshot()) != count {
		t.Fatal("late read resurrected finished request")
	}
}
