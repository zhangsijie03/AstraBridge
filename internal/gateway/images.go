package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"bpslocal/internal/basispoints"
	"bpslocal/internal/identity"
)

const (
	codeImageInvalid      = "invalid_image_input"
	codeUploadFailed      = "image_upload_failed"
	codeUploadTimeout     = "image_upload_timeout"
	codeUploadBusy        = "image_upload_busy"
	attachmentTimeout     = 60 * time.Second
	maxAttachmentResponse = 64 << 10
	// 限制并发大请求的估算内存：请求 JSON、图片字符串和转换副本共用预算。
	bodyMemoryBudget     = 512 << 20
	bodyMemoryMultiplier = 8
)

type attachmentError struct{ status int }

func (e *attachmentError) Error() string {
	return fmt.Sprintf("BPS attachment returned HTTP %d", e.status)
}

func bpsHeaders(account identity.Account) http.Header {
	return http.Header{
		"Authorization": {"Bearer " + account.AccessToken}, "Chatgpt-Account-Id": {account.AccountID}, "X-Openai-Account-Id": {account.AccountID},
		"X-Basispoints-Auth-Mode": {"chatgpt"}, "Content-Type": {"application/json"}, "Accept": {"text/event-stream"},
		"Origin": {"https://bps.openai.com"}, "User-Agent": {"Mozilla/5.0"},
		"X-Openai-Internal-Basispoints-Client-Product": {"basispoints-excel-plugin"}, "X-Openai-Internal-Basispoints-Client-Agent-Profile": {"excel"},
	}
}

// 与上游 openai_excel_bps_attachments.go 相同的 multipart 端点、账号头、
// 60 秒超时和 openai_file_id 契约；仅将平台 HTTP 客户端替换为本机客户端。
func (g *Gateway) uploadAttachment(ctx context.Context, account identity.Account, image basispoints.InlineAttachment) (string, error) {
	if limited := g.coolingAccount(ctx, account.AccountID); limited != nil {
		return "", limited
	}
	ctx, cancel := context.WithTimeout(ctx, attachmentTimeout)
	defer cancel()
	reader, contentType, length, err := image.Multipart()
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, basispoints.AttachmentsURL, reader)
	if err != nil {
		return "", err
	}
	req.Header = bpsHeaders(account)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	req.ContentLength = length
	// 即使宿主客户端被替换，也不允许重定向把账号或图片发送到其他地址。
	client := *g.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	// 先释放附件连接，再发 Responses，避免同一账号的连接槽被自己占用。
	defer resp.Body.Close()
	captureBPSHeaders(ctx, resp)
	if resp.StatusCode == http.StatusTooManyRequests {
		return "", g.recordRateLimit(ctx, account.AccountID, resp.Header.Get("Retry-After"))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		status := resp.StatusCode
		if status < 400 || status > 599 {
			status = http.StatusBadGateway
		}
		return "", &attachmentError{status: status}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAttachmentResponse+1))
	if err != nil {
		return "", err
	}
	if len(raw) > maxAttachmentResponse {
		return "", errors.New("invalid attachment response")
	}
	var result struct {
		OpenAIFileID string `json:"openai_file_id"`
	}
	if json.Unmarshal(raw, &result) != nil || !basispoints.ValidAttachmentID(result.OpenAIFileID) {
		return "", errors.New("invalid attachment response")
	}
	return result.OpenAIFileID, nil
}

func attachmentScope(scope, key string, account identity.Account) string {
	// 换账号、换凭据或换线程后不能复用旧附件；不缓存图片字节或明文凭据。
	if scope == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(scope + "\x00" + key + "\x00" + account.AccountID + "\x00" + account.AccessToken))
	return hex.EncodeToString(sum[:])
}

func uploadFailure(result *Result, requestCtx, upstreamCtx context.Context, err error) {
	setStreamFailure(result, requestCtx, upstreamCtx, err)
	if result.Cancelled {
		return
	}
	if result.Code == codeTimeout {
		result.Code = codeUploadTimeout
		result.Message = "图片上传超时，未发送后续生成请求；请稍后重试。"
		return
	}
	result.Code = codeUploadFailed
	result.Status = http.StatusBadGateway
	var rejected *attachmentError
	switch {
	case errors.As(err, &rejected):
		result.Status = rejected.status
	case errors.Is(err, basispoints.ErrAttachmentBusy):
		result.Code = codeUploadBusy
		result.Status = http.StatusServiceUnavailable
	}
	// 不回显上传响应、附件 ID、图片内容或 HTTP 客户端携带的敏感错误信息。
	result.Message = fmt.Sprintf("BPS 图片上传失败（HTTP %d），未发送后续生成请求。", result.Status)
}

func (g *Gateway) reserveBody(contentLength int64) (int64, bool) {
	size := contentLength
	if size < 0 {
		size = MaxBody
	}
	if size < 1<<20 {
		size = 1 << 20
	}
	reserve := size * bodyMemoryMultiplier
	for {
		used := g.bodyBytes.Load()
		if reserve > bodyMemoryBudget-used {
			return 0, false
		}
		if g.bodyBytes.CompareAndSwap(used, used+reserve) {
			return reserve, true
		}
	}
}
