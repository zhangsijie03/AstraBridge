package gateway

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// LimitHint 仅记录固定名称及验证后的数值；不保留任意响应头或错误正文。
// 这些是上游在该次响应中报告的指标，不能直接推断 BPS 的限流原因。
type LimitHint struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type limitHintKind int

const (
	hintInteger limitHintKind = iota
	hintPercent
	hintDuration
	hintRetryAfter
)

var limitHintHeaders = [...]struct {
	name string
	kind limitHintKind
}{
	{"retry-after", hintRetryAfter},
	{"x-ratelimit-remaining-requests", hintInteger},
	{"x-ratelimit-remaining-tokens", hintInteger},
	{"x-ratelimit-reset-requests", hintDuration},
	{"x-ratelimit-reset-tokens", hintDuration},
	{"x-codex-primary-used-percent", hintPercent},
	{"x-codex-primary-reset-after-seconds", hintInteger},
	{"x-codex-primary-window-minutes", hintInteger},
	{"x-codex-secondary-used-percent", hintPercent},
	{"x-codex-secondary-reset-after-seconds", hintInteger},
	{"x-codex-secondary-window-minutes", hintInteger},
}

func safeLimitHints(header http.Header, now time.Time) []LimitHint {
	var hints []LimitHint
	for _, field := range limitHintHeaders {
		value := strings.TrimSpace(header.Get(field.name))
		if value == "" || len(value) > 128 {
			continue
		}
		var normalized string
		switch field.kind {
		case hintInteger:
			if number, err := strconv.ParseUint(value, 10, 53); err == nil {
				normalized = strconv.FormatUint(number, 10)
			}
		case hintPercent:
			if number, err := strconv.ParseFloat(value, 64); err == nil && !math.IsNaN(number) && number >= 0 && number <= 100 {
				normalized = strconv.FormatFloat(number, 'f', -1, 64)
			}
		case hintDuration:
			if duration, err := time.ParseDuration(value); err == nil && duration >= 0 && duration <= maxRetryAfterHint {
				normalized = duration.String()
			}
		case hintRetryAfter:
			if duration, valid := bpsRetryAfter(value, now); valid && duration >= 0 && duration <= maxRetryAfterHint {
				normalized = duration.String()
			}
		}
		if normalized != "" {
			hints = append(hints, LimitHint{Name: field.name, Value: normalized})
		}
	}
	return hints
}

func captureBPSHeaders(ctx context.Context, response *http.Response) {
	now := time.Now()
	if state := requestRateLimitFrom(ctx); state != nil {
		state.mu.Lock()
		// 每次尝试替换提示，纠错限流不能误用首次成功响应的 Retry-After。
		state.retryAfter = ""
		if _, valid := bpsRetryAfter(response.Header.Get("Retry-After"), now); valid {
			state.retryAfter = strings.TrimSpace(response.Header.Get("Retry-After"))
		}
		state.mu.Unlock()
	}
	traceFrom(ctx).headers(response.StatusCode, safeLimitHints(response.Header, now)...)
}

func (g *Gateway) recordStreamRateLimit(ctx context.Context) {
	if state := requestRateLimitFrom(ctx); state != nil {
		state.mu.Lock()
		accountID, retryAfter := state.accountID, state.retryAfter
		state.mu.Unlock()
		// 分类完全由原生 ParseUpstreamFailure 完成；这里只接入单账号冷却。
		if observedRateLimit(ctx) == nil {
			g.recordRateLimit(ctx, accountID, retryAfter)
		}
	}
}
