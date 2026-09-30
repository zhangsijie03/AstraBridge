package gateway

import (
	"bufio"
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

// EventSource discards comments; the idle timer sees only frames with data.
// This fixture intentionally parses wire frames independently of scanFrames.
func readDataEvents(reader io.Reader, events chan<- string) {
	defer close(events)
	scanner := bufio.NewScanner(reader)
	var data []string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if len(data) > 0 {
				events <- strings.Join(data, "\n")
			}
			data = nil
		} else if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
}

func TestHeartbeatYieldsAnEventWithoutInventingModelOutput(t *testing.T) {
	for _, tc := range []struct {
		name, frame string
		count       int
	}{{"native comment only", ": keepalive\n\n", 0}, {"event compatible", sseHeartbeatFrame, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			events := make(chan string, 2)
			readDataEvents(strings.NewReader(tc.frame), events)
			count := 0
			for data := range events {
				var event map[string]any
				if err := json.Unmarshal([]byte(data), &event); err != nil || len(event) != 1 || event["type"] != "keepalive" {
					t.Fatalf("heartbeat invented a model event: %s (%v)", data, err)
				}
				count++
			}
			if count != tc.count {
				t.Fatalf("EventSource yielded %d events; want %d", count, tc.count)
			}
		})
	}
}

func TestSilentUpstreamKeepsEventIdleTimerAliveAndCancelsCleanly(t *testing.T) {
	upstreamStopped := make(chan struct{})
	g := gateway(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(upstreamStopped)
	})
	g.heartbeatInterval = 15 * time.Millisecond
	var capture traceCapture
	g.SetTraceObserver(capture.add)
	results := make(chan Result, 1)
	g.report = func(result Result) { results <- result }
	local := httptest.NewServer(g)
	defer local.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := request(simpleRequest).WithContext(ctx)
	r.URL.Scheme = "http"
	r.URL.Host = strings.TrimPrefix(local.URL, "http://")
	r.RequestURI = ""
	response, err := local.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	events := make(chan string, 64)
	go readDataEvents(response.Body, events)
	// The upstream remains silent for several event-idle intervals.
	idle := time.NewTimer(120 * time.Millisecond)
	defer idle.Stop()
	observation := time.NewTimer(400 * time.Millisecond)
	defer observation.Stop()
	count := 0
observe:
	for {
		select {
		case data, ok := <-events:
			if !ok {
				t.Fatal("stream disconnected during silent upstream wait")
			}
			var event struct{ Type string }
			if json.Unmarshal([]byte(data), &event) != nil || event.Type != "keepalive" {
				t.Fatalf("silent upstream produced unexpected content: %s", data)
			}
			count++
			idle.Reset(120 * time.Millisecond)
		case <-idle.C:
			t.Fatal("event-level idle timeout despite heartbeat")
		case <-observation.C:
			break observe
		}
	}
	if count < 5 {
		t.Fatalf("too few heartbeat events: %d", count)
	}
	cancel()
	select {
	case <-upstreamStopped:
	case <-time.After(time.Second):
		t.Fatal("cancel did not reach the upstream")
	}
	select {
	case result := <-results:
		if result.Success || !result.Cancelled || result.Code != codeCancelled {
			t.Fatalf("cancel became success or failure: %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("gateway did not terminate after cancellation")
	}
	for _, event := range capture.snapshot() {
		if event.UpstreamBytes != 0 || event.ClientEvents != 0 || event.Stage == traceCompleted {
			t.Fatalf("keepalive was recorded as model progress: %+v", event)
		}
	}
}

func TestHTTPModelUnavailablePreservesStatusWithoutReplay(t *testing.T) {
	for _, tc := range []struct {
		status     int
		code, kind string
	}{{404, "model_not_found", "invalid_request_error"}, {404, "model_not_found_error", "invalid_request_error"},
		{403, "basispoints_model_access_changed", "permission_error"}} {
		t.Run(tc.code, func(t *testing.T) {
			var calls atomic.Int32
			g := gateway(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var body struct{ Model string }
				if json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != "gpt-6-astra" {
					t.Error("fixed upstream model changed")
				}
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{
					"type": tc.kind, "code": tc.code, "message": "SECRET-PROMPT " + token(),
				}})
			})
			var result Result
			var capture traceCapture
			g.report = func(value Result) { result = value }
			g.SetTraceObserver(capture.add)
			w := httptest.NewRecorder()
			g.ServeHTTP(w, request(simpleRequest))
			if calls.Load() != 1 || w.Code != tc.status || result.Status != tc.status || result.Success || result.Code != codeModelUnavailable {
				t.Fatalf("model failure was replayed or misclassified: calls=%d status=%d result=%+v", calls.Load(), w.Code, result)
			}
			var envelope struct {
				Error struct{ Type, Code, Message string }
			}
			if json.Unmarshal(w.Body.Bytes(), &envelope) != nil || envelope.Error.Code != tc.code || envelope.Error.Type != tc.kind || !strings.Contains(envelope.Error.Message, "本机模型名已校验") {
				t.Fatalf("upstream classification was lost: %s", w.Body.String())
			}
			events := capture.snapshot()
			last := events[len(events)-1]
			if last.Stage != traceFailed || last.HTTPStatus != tc.status || !strings.Contains(last.Message, "上游模型暂不可用") {
				t.Fatalf("diagnostic omitted upstream model failure: %+v", last)
			}
			encoded, _ := json.Marshal(struct {
				Result Result
				Events []TraceEvent
			}{result, events})
			for _, secret := range []string{"SECRET-PROMPT", token()} {
				if strings.Contains(w.Body.String(), secret) || strings.Contains(string(encoded), secret) {
					t.Fatal("upstream secrets leaked in diagnostics")
				}
			}
		})
	}
}

func TestUnknown404IsNotMisdiagnosedAsModelUnavailable(t *testing.T) {
	g := gateway(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, "{\"error\":{\"code\":\"route_not_found\"}}")
	})
	var result Result
	g.report = func(value Result) { result = value }
	w := httptest.NewRecorder()
	g.ServeHTTP(w, request(simpleRequest))
	if w.Code != 404 || result.Code == codeModelUnavailable || strings.Contains(w.Body.String(), "本机模型名已校验") {
		t.Fatalf("unrelated 404 misdiagnosed: %+v", result)
	}
}

func TestStreamModelUnavailableIsFailureDespiteHTTP200(t *testing.T) {
	for _, stream := range []bool{true, false} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			var calls atomic.Int32
			g := gateway(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"model_not_found\",\"message\":\"SECRET-PROMPT\"}}}\n\n")
			})
			var result Result
			var capture traceCapture
			g.report = func(value Result) { result = value }
			g.SetTraceObserver(capture.add)
			w := httptest.NewRecorder()
			g.ServeHTTP(w, request(fmt.Sprintf("{\"model\":\"gpt-6-astra\",\"input\":\"hello\",\"stream\":%t}", stream)))
			if result.Success || result.Code != codeModelUnavailable || result.Status != 404 || calls.Load() != 1 {
				t.Fatalf("stream error treated as success or replayed: %+v", result)
			}
			wantHTTP := 404
			if stream {
				wantHTTP = 200
			}
			if w.Code != wantHTTP || strings.Contains(w.Body.String(), "SECRET-PROMPT") || strings.Contains(w.Body.String(), "response.completed") {
				t.Fatalf("incorrect terminal response: %d %s", w.Code, w.Body.String())
			}
			events := capture.snapshot()
			last := events[len(events)-1]
			if last.Stage != traceFailed || last.HTTPStatus != 200 || last.SemanticStatus != 404 {
				t.Fatalf("wire status and semantic status were conflated: %+v", last)
			}
		})
	}
}
