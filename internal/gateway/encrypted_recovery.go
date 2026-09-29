package gateway

import (
	"bytes"
	"context"
	"io"
	"net/http"

	"bpslocal/internal/identity"
)

// 原生 v2.9.3 的单次同路恢复：只处理 HTTP 400 明确拒绝的加密 reasoning。
// 先释放旧响应，再发修正请求；不能丢弃 compaction、用户正文或工具结果。
func (g *Gateway) recoverEncrypted(ctx context.Context, response *http.Response, prepared []byte, account identity.Account) (*http.Response, []byte, error) {
	const maxRejectionBytes = 512 << 10
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, maxRejectionBytes+1))
	_ = response.Body.Close()
	response.Body = io.NopCloser(bytes.NewReader(raw))
	body, retry := prepareExcelBPSInvalidEncryptedRetry(prepared, raw)
	if !retry || readErr != nil || len(raw) > maxRejectionBytes || ctx.Err() != nil {
		return response, prepared, nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, g.endpoint, bytes.NewReader(body))
	if err != nil {
		return response, prepared, err
	}
	request.Header = bpsHeaders(account)
	trace := traceFrom(ctx)
	trace.attempt(false)
	trace.stage(traceRepair, "上游拒绝加密推理，按原生规则移除不可读推理并重试一次")
	retried, err := g.client.Do(request)
	if err != nil {
		return retried, body, err
	}
	trace.headers(retried.StatusCode)
	retried.Body = traceBody(ctx, retried.Body)
	return retried, body, nil
}
