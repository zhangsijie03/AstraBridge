package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"bpslocal/internal/basispoints"
)

type TraceStage string

const (
	tracePrepare   TraceStage = "prepare"
	traceUpload    TraceStage = "upload"
	traceConnect   TraceStage = "connect"
	traceHeaders   TraceStage = "headers"
	traceStreaming TraceStage = "streaming"
	traceRepair    TraceStage = "repair"
	traceCompleted TraceStage = "completed"
	traceFailed    TraceStage = "failed"
	traceCancelled TraceStage = "cancelled"
)
const traceInterval = 5 * time.Second

// 诊断仅包含本地标识、阶段、计数及校验过的数值提示，禁止加入正文及账号信息。
type TraceEvent struct {
	RequestID      string      `json:"request_id"`
	Time           string      `json:"time"`
	Stage          TraceStage  `json:"stage"`
	Message        string      `json:"message"`
	ElapsedMS      int64       `json:"elapsed_ms"`
	QuietMS        int64       `json:"quiet_ms"`
	UpstreamBytes  int64       `json:"upstream_bytes"`
	ClientEvents   int64       `json:"client_events"`
	ToolCalls      int64       `json:"tool_calls"`
	Attempt        int         `json:"attempt"`
	HTTPStatus     int         `json:"http_status,omitempty"`
	SemanticStatus int         `json:"semantic_status,omitempty"`
	LimitHints     []LimitHint `json:"limit_hints,omitempty"`
}
type traceKey struct{}

var traceSequence atomic.Uint64

type requestTrace struct {
	failureDetail                 string
	toolRepair                    bool
	finished                      bool
	mu                            sync.Mutex
	start, lastByte, attemptStart time.Time
	event                         TraceEvent
	observer                      func(TraceEvent)
	done                          chan struct{}
	stopped                       chan struct{}
}

// SetTraceObserver must be called before serving requests. The observer must
// enqueue quickly; a slow UI must never block upstream forwarding.
func (g *Gateway) SetTraceObserver(observer func(TraceEvent)) { g.traceObserver = observer }
func (g *Gateway) beginTrace(ctx context.Context) (context.Context, *requestTrace) {
	if g.traceObserver == nil {
		return ctx, nil
	}
	now := time.Now()
	t := &requestTrace{start: now, attemptStart: now, observer: g.traceObserver, done: make(chan struct{}), stopped: make(chan struct{}), event: TraceEvent{RequestID: fmt.Sprintf("R%06d", traceSequence.Add(1)), Stage: tracePrepare, Message: "收到客户端请求，正在读取账号与请求体"}}
	t.publish()
	go func() {
		defer close(t.stopped)
		tick := time.NewTicker(traceInterval)
		defer tick.Stop()
		for {
			select {
			case <-t.done:
				return
			case <-ctx.Done():
				return
			case <-tick.C:
				t.publish()
			}
		}
	}()
	return context.WithValue(ctx, traceKey{}, t), t
}
func traceFrom(ctx context.Context) *requestTrace {
	t, _ := ctx.Value(traceKey{}).(*requestTrace)
	return t
}
func (t *requestTrace) publish() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.finished {
		return
	}
	t.publishLocked()
}
func (t *requestTrace) publishLocked() {
	now := time.Now()
	e := t.event
	e.Time = now.Format(time.RFC3339)
	e.ElapsedMS = now.Sub(t.start).Milliseconds()
	reference := t.lastByte
	if reference.IsZero() {
		reference = t.attemptStart
	}
	e.QuietMS = now.Sub(reference).Milliseconds()
	t.observer(e)
}
func (t *requestTrace) stage(stage TraceStage, message string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.finished {
		return
	}
	t.event.Stage = stage
	t.event.Message = message
	t.publishLocked()
}
func (t *requestTrace) attempt(repair bool) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.finished {
		return
	}
	t.toolRepair = repair
	t.event.Attempt++
	t.event.HTTPStatus = 0
	t.event.LimitHints = nil
	t.attemptStart = time.Now()
	t.lastByte = time.Time{}
	t.event.Stage = traceConnect
	t.event.Message = "正在连接 BPS / 等待响应头"
	if repair {
		t.event.Stage = traceRepair
		t.event.Message = "正在请求 BPS 工具纠错，尚未下发工具"
	}
	t.publishLocked()
}
func (t *requestTrace) headers(status int, hints ...LimitHint) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.finished {
		return
	}
	t.event.HTTPStatus = status
	t.event.LimitHints = hints
	t.event.Stage = traceHeaders
	t.event.Message = "已收到 BPS 响应头，等待响应数据"
	t.publishLocked()
}
func (t *requestTrace) forwarded(sent, tool bool) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.finished {
		return
	}
	if sent {
		t.event.ClientEvents++
	}
	if tool {
		t.event.ToolCalls++
	}
}
func (t *requestTrace) finish(ctx context.Context, success bool) {
	if t == nil {
		return
	}
	close(t.done)
	<-t.stopped
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.finished {
		return
	}
	t.finished = true
	switch {
	case ctx.Err() != nil:
		t.event.Stage = traceCancelled
		t.event.Message = "请求已取消或超时，转发已结束"
	case success:
		t.event.Stage = traceCompleted
		t.event.Message = "本次响应已完整返回客户端"
		if t.event.ToolCalls > 0 {
			t.event.Message = "工具调用已返回客户端；后续工具执行及下一轮请求由客户端处理"
		}
	default:
		t.event.Stage = traceFailed
		t.event.Message = "本次转发未完成，请结合主窗口错误信息排查"
		if t.failureDetail != "" {
			t.event.Message = t.failureDetail
		}
	}
	t.publishLocked()
}

type tracedBody struct {
	io.ReadCloser
	trace *requestTrace
}

func (b *tracedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		t := b.trace
		t.mu.Lock()
		if t.finished {
			t.mu.Unlock()
			return n, err
		}
		first := t.lastByte.IsZero()
		t.lastByte = time.Now()
		t.event.UpstreamBytes += int64(n)
		t.event.Stage = traceStreaming
		t.event.Message = "正在接收 BPS 数据（可能包含推理或待校验工具）"
		if t.toolRepair {
			t.event.Message = "正在接收工具纠错数据，等待原生协议校验"
		}
		if first {
			t.publishLocked()
		}
		t.mu.Unlock()
	}
	return n, err
}
func traceBody(ctx context.Context, body io.ReadCloser) io.ReadCloser {
	if t := traceFrom(ctx); t != nil {
		return &tracedBody{body, t}
	}
	return body
}

// 只记录固定终止类型和允许的错误分类，绝不把上游正文或错误消息写入日志。
func (t *requestTrace) terminalFailure(kind string, response json.RawMessage) {
	if t == nil {
		return
	}
	code := nativeFailureCode(response)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.failureDetail = "原生响应未完成：" + kind + " · " + code
}

func nativeFailureCode(payload json.RawMessage) string {
	if failure := basispoints.ParseUpstreamFailure(payload); failure != nil {
		return failure.Code
	}
	type failure struct {
		Code string `json:"code"`
	}
	var value struct {
		Error    failure `json:"error"`
		Response struct {
			Error failure `json:"error"`
		} `json:"response"`
	}
	_ = json.Unmarshal(payload, &value)
	code := value.Response.Error.Code
	if code == "" {
		code = value.Error.Code
	}
	switch code {
	case "basispoints_protocol_error", "invalid_encrypted_content", "rate_limit_exceeded", "server_error":
		return code
	default:
		return "upstream_reported_error"
	}
}

// 终止分类独立于 UI 文案，避免网络错误和超时在导出日志里只剩“未完成”。
func (t *requestTrace) result(result Result) {
	if t == nil || result.Success || result.Code == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.event.SemanticStatus = result.Status
	if result.Code == codeModelUnavailable {
		t.failureDetail = "上游模型暂不可用 · " + result.UpstreamCode + "；本机模型名已校验，未重放请求"
	} else if result.Code == codeRateLimited {
		// 只写本地产生的冷却信息，不复制上游错误正文；HTTPStatus 仍保留真实上游状态。
		t.failureDetail = result.Message + " · " + codeRateLimited
		if result.UpstreamCode != "" {
			t.failureDetail += " · " + result.UpstreamCode
		}
	} else if t.failureDetail == "" {
		t.failureDetail = "转发未完成 · " + result.Code
	}
}
