package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"bpslocal/internal/basispoints"
	"bpslocal/internal/identity"
)

const MaxBody = 64 << 20

type Result struct {
	Cancelled bool     `json:"cancelled,omitempty"`
	Message   string   `json:"message,omitempty"`
	Success   bool     `json:"success"`
	Model     string   `json:"model,omitempty"`
	Effort    string   `json:"effort,omitempty"`
	Code      string   `json:"code,omitempty"`
	Status    int      `json:"status,omitempty"`
	Account   string   `json:"account,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
}
type Gateway struct {
	model             string
	accountSource     func() (identity.Account, error)
	heartbeatInterval time.Duration
	requestTimeout    time.Duration
	key               string
	endpoint          string
	client            *http.Client
	replay            basispoints.ReplayCache
	attachments       basispoints.AttachmentCache
	bodyBytes         atomic.Int64
	report            func(Result)
	slots             chan struct{}
}

func New(key, model string, accountSource func() (identity.Account, error), report func(Result)) *Gateway {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 45 * time.Second
	transport.MaxIdleConnsPerHost = 8
	return &Gateway{model: model, accountSource: accountSource, heartbeatInterval: 15 * time.Second, requestTimeout: 20 * time.Minute, key: key, endpoint: basispoints.ResponsesURL, client: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, report: report, slots: make(chan struct{}, 8)}
}
func (g *Gateway) emit(r Result) {
	if g.report != nil {
		g.report(r)
	}
}
func problem(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": map[string]string{"type": "invalid_request_error", "code": code, "message": message}})
}
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 客户端只持有中转 API Key；上游账号凭据由服务端读取，禁止客户端覆盖。
	host, _, e := net.SplitHostPort(r.Host)
	if e != nil {
		host = r.Host
	}
	if host != "127.0.0.1" || r.Header.Get("Origin") != "" || g.key == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+g.key)) != 1 {
		problem(w, 403, "local_access_denied", "本地访问验证失败")
		return
	}
	if r.Method == "GET" && r.URL.Path == "/v1/models" {
		w.Header().Set("Content-Type", "application/json")
		// 仅列出用户配置的模型，不声称已查询账号权限或上游完整模型目录。
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"object": "list", "data": []map[string]interface{}{{"id": g.model, "object": "model", "created": 0, "owned_by": "bps-local"}}})
		return
	}
	if r.Method != "POST" || (r.URL.Path != "/v1/responses" && r.URL.Path != "/v1/responses/compact") {
		problem(w, 404, "unsupported_endpoint", "AstraBridge 仅支持 Responses 和 compact 接口")
		return
	}
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if media != "application/json" {
		problem(w, 415, "invalid_content_type", "请求必须为 application/json")
		return
	}
	if g.accountSource == nil {
		problem(w, 503, "account_unavailable", "服务端尚未配置上游账号")
		return
	}
	account, e := g.accountSource()
	if e != nil {
		problem(w, 401, "upstream_login_required", e.Error())
		g.emit(Result{Code: "upstream_login_required", Status: 401, Message: e.Error()})
		return
	}
	select {
	case g.slots <- struct{}{}:
		defer func() { <-g.slots }()
	default:
		problem(w, 429, "local_concurrency_limit", "本地并发请求已达上限，请稍后重试")
		return
	}
	if r.ContentLength > MaxBody {
		problem(w, 413, "body_too_large", "请求体超过 64 MiB")
		return
	}
	reserved, admitted := g.reserveBody(r.ContentLength)
	if !admitted {
		w.Header().Set("Retry-After", "1")
		problem(w, 503, "local_memory_limit", "正在处理较大的请求，请稍后重试")
		return
	}
	defer g.bodyBytes.Add(-reserved)
	r.Body = http.MaxBytesReader(w, r.Body, MaxBody)
	raw, e := io.ReadAll(r.Body)
	if e != nil {
		problem(w, 413, "body_too_large", "请求体超过 64 MiB 或无法读取")
		return
	}
	var source map[string]json.RawMessage
	if json.Unmarshal(raw, &source) != nil || source == nil {
		problem(w, 400, "invalid_json", "无效的 JSON 请求")
		return
	}
	var model string
	_ = json.Unmarshal(source["model"], &model)
	if g.model != "" && model != g.model {
		problem(w, 400, "unsupported_model", "AstraBridge 仅支持固定模型 "+g.model)
		g.emit(Result{Code: "unsupported_model", Status: 400, Model: model, Message: "AstraBridge 仅支持固定模型 " + g.model})
		return
	}
	var stream bool
	_ = json.Unmarshal(source["stream"], &stream)
	if r.URL.Path == "/v1/responses/compact" {
		var input []json.RawMessage
		if json.Unmarshal(source["input"], &input) != nil {
			var text string
			if json.Unmarshal(source["input"], &text) != nil {
				problem(w, 400, "invalid_compact_input", "压缩请求缺少有效历史")
				return
			}
			msg, _ := json.Marshal(map[string]string{"role": "user", "content": text})
			input = []json.RawMessage{msg}
		}
		input = append(input, json.RawMessage(`{"type":"compaction_trigger"}`))
		source["input"], _ = json.Marshal(input)
		source["tool_choice"] = json.RawMessage(`"none"`)
		raw, _ = json.Marshal(source)
		stream = false
	}
	scope := requestScope(r, source, account.AccountID)
	// 所有图片及完整请求先在本地校验，避免无效工具或历史导致图片先被上传。
	images, e := basispoints.PrepareNativeImages(raw)
	if e != nil {
		problem(w, 400, codeImageInvalid, e.Error())
		g.emit(Result{Code: codeImageInvalid, Status: 400, Model: model, Message: "图片格式或大小不符合要求；支持 PNG、JPEG、GIF、WebP，单张最多 20 MiB。"})
		return
	}
	preflight, e := images.Body()
	if e != nil {
		problem(w, 400, codeImageInvalid, "无法解析图片请求")
		return
	}
	prepared, bridge, e := basispoints.Prepare(preflight, scope, &g.replay)
	if e != nil {
		problem(w, 400, "unsupported_bps_request", e.Error())
		g.emit(Result{Code: codeUnsupported, Status: 400, Model: model, Message: unsupportedMessage(raw)})
		return
	}
	result := Result{Model: model, Effort: bridge.Effort, Account: account.MaskedEmail, Warnings: bridge.Warnings}
	defer func() {
		if !result.Success && (r.Context().Err() != nil || result.Cancelled) {
			result.Cancelled = true
			result.Code = codeCancelled
			result.Status = 0
			result.Message = "客户端已停止或取消本次请求；BPS 仍可继续使用。"
		}
		g.emit(result)
	}()
	// 完整请求超时有上界；客户端取消会沿同一 context 立即传递至上游。
	ctx, cancel := context.WithTimeout(r.Context(), g.requestTimeout)
	defer cancel()
	if images.HasImages() {
		raw, e = images.Upload(ctx, &g.attachments, attachmentScope(scope, g.key, account), func(uploadCtx context.Context, image basispoints.InlineAttachment) (string, error) {
			return g.uploadAttachment(uploadCtx, account, image)
		})
		if e != nil {
			uploadFailure(&result, r.Context(), ctx, e)
			if !result.Cancelled {
				problem(w, result.Status, result.Code, result.Message)
			}
			return
		}
		prepared, bridge, e = basispoints.Prepare(raw, scope, &g.replay)
		if e != nil {
			result.Code = codeUnsupported
			result.Status = 400
			result.Message = "上传后的图片请求无法转换；未发送生成请求。"
			problem(w, result.Status, result.Code, result.Message)
			return
		}
	}
	upstream, e := http.NewRequestWithContext(ctx, "POST", g.endpoint, bytes.NewReader(prepared))
	if e != nil {
		result.Code = "request_build_failed"
		problem(w, 502, result.Code, "无法创建上游请求")
		return
	}
	upstream.Header = bpsHeaders(account)
	resp, e := g.client.Do(upstream)
	if e != nil {
		setStreamFailure(&result, r.Context(), ctx, e)
		if result.Cancelled {
			return
		}
		if result.Code != codeTimeout {
			result.Code = codeConnection
			result.Status = 502
			result.Message = "无法连接 BPS 上游，请检查网络或代理；请求未自动重放。"
		}
		problem(w, result.Status, result.Code, result.Message)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		result.Status = resp.StatusCode
		result.Code = "upstream_rejected"
		// 上游可能回显用户输入，因此既不打印错误正文，也不直接返回给客户端。
		status := resp.StatusCode
		if status < 400 {
			status = 502
		}
		result.Message = fmt.Sprintf("BPS 上游返回 HTTP %d；请检查登录状态、模型权限或稍后重试。", resp.StatusCode)
		problem(w, status, result.Code, result.Message)
		return
	}
	contentType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if contentType != "text/event-stream" {
		result.Code = "unexpected_upstream_format"
		problem(w, 502, result.Code, "BPS 上游未返回事件流")
		return
	}
	converted := bridge.Stream(resp.Body)
	defer converted.Close()
	g.forwardStream(w, r, ctx, converted, stream, &result)
}

// 不把适配器错误原文放入 UI；它可能包含客户端参数或工具内容。
func unsupportedMessage(raw []byte) string {
	var request struct {
		Text struct {
			Format struct {
				Type string `json:"type"`
			} `json:"format"`
		} `json:"text"`
	}
	_ = json.Unmarshal(raw, &request)
	if request.Text.Format.Type != "" && request.Text.Format.Type != "text" {
		return "Sub2API v2.8.11 原版不支持结构化格式；Codex 自动标题等请求可能因此失败，这不是账号权限异常。"
	}
	return "此请求包含 BPS 不支持的参数、图片、工具或历史格式；这不代表账号权限异常。"
}

var unscopedSequence atomic.Uint64

func requestScope(r *http.Request, source map[string]json.RawMessage, account string) string {
	// 子线程优先，不能把所有共享父 session_id 的子代理放入同一个回放缓存。
	thread := r.Header.Get("thread-id")
	if thread == "" {
		var v struct {
			ThreadID string `json:"thread_id"`
		}
		if json.Unmarshal([]byte(r.Header.Get("x-codex-turn-metadata")), &v) == nil {
			thread = v.ThreadID
		}
	}
	identity := "thread:" + thread
	if thread == "" {
		identity = fmt.Sprintf("isolated-request:%d", unscopedSequence.Add(1))
	}
	// 没有可信线程 ID 时隔离每次请求，缺失历史宁可报错，也不能串用工具结果。
	sum := sha256.Sum256([]byte(account + "\x00" + identity))
	return hex.EncodeToString(sum[:])
}
