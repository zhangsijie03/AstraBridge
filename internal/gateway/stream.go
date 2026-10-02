package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"bpslocal/internal/basispoints"
)

const (
	maxStreamFrame       = 16 << 20
	codeCancelled        = "client_cancelled"
	codeTimeout          = "upstream_timeout"
	codeIncomplete       = "upstream_stream_incomplete"
	codeConnection       = "upstream_connection_failed"
	codeBackoff          = "upstream_connection_backoff"
	codeResponseFailed   = "bps_response_failed"
	codeUnsupported      = "unsupported_bps_request"
	codeModelUnavailable = "basispoints_model_unavailable"
	// 原生 BPS 注释心跳只能维持字节链路；Codex 在 eventsource.next() 外计时，
	// 注释不会重置其空闲计时。补充无业务含义的事件，不伪造文本、工具或生命周期。
	sseHeartbeatFrame = ": keepalive\nevent: keepalive\ndata: {\"type\":\"keepalive\"}\n\n"
)

type streamFrame struct {
	text string
	err  error
}

// 按完整事件转发，心跳不会插到 event/data 中间；取消同时释放读流 goroutine。
func scanFrames(ctx context.Context, reader io.Reader) <-chan streamFrame {
	out := make(chan streamFrame)
	go func() {
		defer close(out)
		send := func(frame streamFrame) bool {
			select {
			case out <- frame:
				return true
			case <-ctx.Done():
				return false
			}
		}
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 64*1024), maxStreamFrame)
		var frame strings.Builder
		for scanner.Scan() {
			line := scanner.Text()
			if frame.Len()+len(line)+1 > maxStreamFrame {
				send(streamFrame{err: bufio.ErrTooLong})
				return
			}
			frame.WriteString(line)
			frame.WriteByte('\n')
			if line == "" {
				if !send(streamFrame{text: frame.String()}) {
					return
				}
				frame.Reset()
			}
		}
		if err := scanner.Err(); err != nil {
			send(streamFrame{err: err})
			return
		}
		if frame.Len() != 0 {
			send(streamFrame{err: io.ErrUnexpectedEOF})
		}
	}()
	return out
}
func setStreamFailure(result *Result, requestCtx, upstreamCtx context.Context, err error) {
	result.Code = codeIncomplete
	result.Message = "BPS 连接在响应完成前中断，请检查网络或代理后重试。"
	var timeout net.Error
	switch {
	case requestCtx.Err() != nil:
		result.Code = codeCancelled
		result.Cancelled = true
		result.Status = 0
		result.Message = "客户端已停止或取消本次请求；BPS 仍可继续使用。"
	case upstreamCtx.Err() == context.DeadlineExceeded || (errors.As(err, &timeout) && timeout.Timeout()):
		result.Code = codeTimeout
		result.Status = 504
		result.Message = "等待 BPS 响应超时；请求未自动重放，可稍后重试。"
	}
}

func transientStreamFailure(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var networkErr net.Error
	return errors.As(err, &networkErr)
}
func writeFrame(w http.ResponseWriter, frame string) error {
	// 慢速或已离线的客户端不能无限占用转发任务。测试 recorder 不支持此接口。
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(15 * time.Second))
	_, err := io.WriteString(w, frame)
	if err == nil {
		err = http.NewResponseController(w).Flush()
	}
	return err
}
func failedFrame(code, message string) string {
	raw, _ := json.Marshal(map[string]interface{}{"type": "response.failed", "response": map[string]interface{}{"status": "failed", "output": []interface{}{}, "error": map[string]string{"code": code, "message": message}}})
	return "event: response.failed\ndata: " + string(raw) + "\n\n"
}
func (g *Gateway) forwardStream(w http.ResponseWriter, r *http.Request, ctx context.Context, body io.Reader, stream bool, result *Result, citationRewriter *citationRewriter) {
	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		if err := writeFrame(w, sseHeartbeatFrame); err != nil {
			result.Code = codeCancelled
			result.Cancelled = true
			return
		}
	}
	ticker := time.NewTicker(g.heartbeatInterval)
	defer ticker.Stop()
	frames := scanFrames(ctx, body)
	for {
		var frame streamFrame
		var ok bool
		select {
		case <-ctx.Done():
			setStreamFailure(result, r.Context(), ctx, ctx.Err())
			if !result.Cancelled {
				if stream {
					_ = writeFrame(w, rewriteSSEFrame(failedFrame(result.Code, result.Message), citationRewriter))
				} else {
					problem(w, 504, result.Code, result.Message)
				}
			}
			return
		case <-ticker.C:
			if stream {
				if err := writeFrame(w, sseHeartbeatFrame); err != nil {
					result.Code = codeCancelled
					result.Cancelled = true
					return
				}
			}
			continue
		case frame, ok = <-frames:
		}
		if !ok || frame.err != nil {
			setStreamFailure(result, r.Context(), ctx, frame.err)
			if !result.Cancelled && transientStreamFailure(frame.err) {
				delay := g.upstreamBackoff.failure()
				seconds := backoffSeconds(delay)
				result.Message = fmt.Sprintf("BPS 连接在响应完成前中断，代理正在冷却；请 %d 秒后重试。", seconds)
				if !stream {
					w.Header().Set("Retry-After", strconv.Itoa(seconds))
				}
			}
			if !result.Cancelled {
				if stream {
					_ = writeFrame(w, rewriteSSEFrame(failedFrame(result.Code, result.Message), citationRewriter))
				} else {
					problem(w, 502, result.Code, result.Message)
				}
			}
			return
		}
		frame.text = rewriteSSEFrame(frame.text, citationRewriter)
		terminal := ""
		toolDone := false
		var final json.RawMessage
		var failurePayload json.RawMessage
		var upstreamFailure *basispoints.UpstreamFailure
		for _, line := range strings.Split(frame.text, "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var event struct {
				Type     string          `json:"type"`
				Response json.RawMessage `json:"response"`
				Item     struct {
					Type string `json:"type"`
				} `json:"item"`
			}
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event) != nil {
				continue
			}
			toolDone = event.Type == "response.output_item.done" && (event.Item.Type == "function_call" || event.Item.Type == "custom_tool_call")
			switch event.Type {
			case "response.completed", "response.failed", "response.cancelled", "response.incomplete", "error":
				terminal = event.Type
				final = event.Response
				failurePayload = json.RawMessage(strings.TrimPrefix(line, "data: "))
			}
		}
		if terminal != "" && terminal != "response.completed" {
			result.Code = codeResponseFailed
			result.Status = http.StatusBadGateway
			// 直接采用原生错误分类与脱敏，不再通过第二套 SSE 解析器猜测限流。
			traceFrom(ctx).terminalFailure(terminal, failurePayload)
			result.UpstreamCode = nativeFailureCode(failurePayload)
			result.Message = "原生 BPS 响应未完成：" + terminal + " · " + result.UpstreamCode
			upstreamFailure = basispoints.ParseUpstreamFailure(failurePayload)
			if upstreamFailure != nil {
				result.Status = upstreamFailure.Status
				result.UpstreamType = upstreamFailure.Type
				if upstreamFailure.Status == http.StatusTooManyRequests {
					g.recordStreamRateLimit(ctx)
				}
				setModelUnavailable(result)
			}
			if limited := observedRateLimit(ctx); limited != nil {
				limited.apply(result)
			}
		}
		if stream {
			if err := writeFrame(w, frame.text); err != nil {
				result.Code = codeCancelled
				result.Cancelled = true
				return
			}
		}
		traceFrom(ctx).forwarded(stream, toolDone)
		if terminal == "" {
			continue
		}
		if terminal != "response.completed" {
			if !stream {
				if upstreamFailure != nil {
					if limited := observedRateLimit(ctx); limited != nil && upstreamFailure.Status == http.StatusTooManyRequests {
						w.Header().Set("Retry-After", limited.retryAfter)
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(upstreamFailure.Status)
					_ = json.NewEncoder(w).Encode(map[string]any{"error": upstreamFailure.Details()})
				} else if limited := observedRateLimit(ctx); limited != nil {
					limited.respond(w)
				} else {
					problem(w, 502, result.Code, result.Message)
				}
			}
			return
		}
		if !stream {
			w.Header().Set("Content-Type", "application/json")
			if _, err := w.Write(final); err != nil {
				result.Code = codeCancelled
				result.Cancelled = true
				return
			}
		}
		if !stream {
			traceFrom(ctx).forwarded(true, false)
		}
		result.Success = true
		result.Status = 200
		return
	}
}
