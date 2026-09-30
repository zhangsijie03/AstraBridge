package gateway

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"

	"bpslocal/internal/basispoints"
	"bpslocal/internal/identity"
)

// 对应 Sub2API v2.9.4 openai_excel_bps.go 的 StreamWithRepairs 接入。
// 纠错资格、次数、整批校验、原始代码绑定和用量合并完全由原生模块负责；
// 宿主只提供同账号、同模型、同会话的 HTTP 回调，不重放网络失败。
func (g *Gateway) streamWithRepairs(ctx context.Context, bridge *basispoints.Bridge, upstream io.ReadCloser, prepared []byte, account identity.Account) io.ReadCloser {
	return bridge.StreamWithRepairs(ctx, upstream, func(repairCtx context.Context, failed map[string]any, validation error) (map[string]any, error) {
		body, err := basispoints.BuildToolRepairRequest(prepared, failed, validation)
		if err != nil {
			return nil, err
		}
		response, err := g.openToolCorrection(repairCtx, body, account)
		if err != nil {
			return nil, err
		}
		defer response.Close()
		stop := context.AfterFunc(repairCtx, func() { _ = response.Close() })
		defer stop()
		// 第二次纠错必须接续第一次的原生历史，不能重放最初请求。
		prepared = body
		return basispoints.ReadToolRepairResponse(response)
	}, func(repairCtx context.Context) (io.ReadCloser, error) {
		body, err := basispoints.RepairRequest(prepared)
		if err != nil {
			return nil, err
		}
		return g.openToolCorrection(repairCtx, body, account)
	})
}

func (g *Gateway) openToolCorrection(ctx context.Context, body []byte, account identity.Account) (io.ReadCloser, error) {
	if limited := g.coolingAccount(ctx, account.AccountID); limited != nil {
		return nil, limited
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, g.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("BPS correction request could not be built")
	}
	request.Header = bpsHeaders(account)
	traceFrom(ctx).attempt(true)
	response, err := g.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// 网络错误可能带 URL 或代理凭据，禁止写回客户端错误事件。
		return nil, fmt.Errorf("BPS correction connection failed")
	}
	captureBPSHeaders(ctx, response)
	if response.StatusCode == http.StatusTooManyRequests {
		limited := g.recordRateLimit(ctx, account.AccountID, response.Header.Get("Retry-After"))
		_ = response.Body.Close()
		return nil, limited
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_ = response.Body.Close()
		return nil, fmt.Errorf("BPS correction returned HTTP %d", response.StatusCode)
	}
	contentType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if contentType != "text/event-stream" {
		_ = response.Body.Close()
		return nil, fmt.Errorf("BPS correction did not return an event stream")
	}
	return traceBody(ctx, response.Body), nil
}
