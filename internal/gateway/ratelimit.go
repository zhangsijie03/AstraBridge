package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	codeRateLimited         = "basispoints_rate_limited"
	typeRateLimited         = "rate_limit_error"
	codeUpstreamRateLimited = "rate_limit_exceeded"
	// 与 Sub2API v2.9.4 的 429 默认回避及上限一致，不推断账号额度恢复时间。
	defaultBPSCooldown = 5 * time.Second
	maxBPSCooldown     = 2 * time.Hour
	maxRetryAfterHint  = 7 * 24 * time.Hour
)

type cooldownSource string

const (
	cooldownDefault  cooldownSource = "default_backoff"
	cooldownUpstream cooldownSource = "upstream_retry_after"
)

type cooldownWindow struct {
	until   time.Time
	source  cooldownSource
	retryAt time.Time
}

// RateLimits 只保存当前进程的账号摘要与 BPS 冷却，不保存令牌或修改账号状态。
// 控制器可跨停止/启动共享此状态，换账号和令牌刷新不会串用或绕过冷却。
type RateLimits struct {
	mu    sync.Mutex
	until map[[32]byte]cooldownWindow
	now   func() time.Time
}

func (s *RateLimits) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// 原生规则接受秒数和 HTTP 日期；只允许校验过的 Retry-After 返回客户端。
func bpsRetryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return 0, false
	}
	if seconds, err := strconv.ParseUint(value, 10, 32); err == nil {
		return time.Duration(seconds) * time.Second, true
	}
	if date, err := http.ParseTime(value); err == nil {
		return date.Sub(now), true
	}
	return 0, false
}

type bpsRateLimitError struct {
	until      time.Time
	seconds    int
	retryAfter string
	local      bool
	source     cooldownSource
	retryAt    time.Time
}

func (e *bpsRateLimitError) Error() string {
	prefix := "BPS 上游限流"
	if e.local {
		prefix = "本地冷却拦截，未再向 BPS 发送请求"
	}
	wait := "上游未提供有效等待时间，采用本地默认回避"
	if e.source == cooldownUpstream {
		wait = "按上游 Retry-After 暂停（本地上限 2 小时）"
		if !e.retryAt.IsZero() {
			wait = "上游建议不早于 " + e.retryAt.Local().Format("01-02 15:04:05") + " 重试"
		}
	}
	return fmt.Sprintf("%s；%s。本地暂停至 %s（记录时剩余 %d 秒），不代表额度已恢复。", prefix, wait, e.until.Local().Format("15:04:05"), e.seconds)
}

func (e *bpsRateLimitError) apply(result *Result) {
	result.Code, result.Status, result.Message = codeRateLimited, http.StatusTooManyRequests, e.Error()
	result.UpstreamType = typeRateLimited
}

func (e *bpsRateLimitError) respond(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", e.retryAfter)
	w.WriteHeader(http.StatusTooManyRequests)
	_ = json.NewEncoder(w).Encode(map[string]map[string]string{"error": {"type": typeRateLimited, "code": codeRateLimited, "message": e.Error()}})
}

func (s *RateLimits) window(accountID, retryAfter string, observed bool) *bpsRateLimitError {
	now := s.clock()
	key := sha256.Sum256([]byte(accountID))
	s.mu.Lock()
	defer s.mu.Unlock()
	// 本机只能使用可信账号来源，顺便清理已过期记录，避免历史账号无限累积。
	for id, window := range s.until {
		if !now.Before(window.until) {
			delete(s.until, id)
		}
	}
	window := s.until[key]
	validHeader := false
	if observed {
		duration, valid := bpsRetryAfter(retryAfter, now)
		validHeader = valid
		next := cooldownWindow{source: cooldownDefault}
		if valid {
			next.source = cooldownUpstream
			if duration >= 0 && duration <= maxRetryAfterHint {
				next.retryAt = now.Add(duration)
			}
		}
		if !valid {
			duration = defaultBPSCooldown
		}
		if duration < time.Second {
			duration = time.Second
		}
		if duration > maxBPSCooldown {
			duration = maxBPSCooldown
		}
		next.until = now.Add(duration)
		if next.until.After(window.until) || (next.until.Equal(window.until) && next.retryAt.After(window.retryAt)) {
			window = next
		}
		if s.until == nil {
			s.until = make(map[[32]byte]cooldownWindow)
		}
		s.until[key] = window
	}
	until := window.until
	if !now.Before(until) {
		return nil
	}
	seconds := int((until.Sub(now) + time.Second - 1) / time.Second)
	header := strconv.Itoa(seconds)
	// 本地调度上限仍沿用原生规则；客户端等待提示不能早于已知的上游建议。
	if window.retryAt.After(until) {
		header = strconv.FormatInt(int64((window.retryAt.Sub(now)+time.Second-1)/time.Second), 10)
	}
	// 原生会保留合法上游 Retry-After；若它比已有冷却短，则返回剩余冷却。
	if validHeader {
		if duration, _ := bpsRetryAfter(retryAfter, now); duration >= until.Sub(now) && duration <= maxRetryAfterHint && !now.Add(duration).Before(window.retryAt) {
			header = strings.TrimSpace(retryAfter)
		}
	}
	return &bpsRateLimitError{until: until, seconds: seconds, retryAfter: header, local: !observed, source: window.source, retryAt: window.retryAt}
}

type rateLimitContextKey struct{}
type requestRateLimit struct {
	mu         sync.Mutex
	signal     *bpsRateLimitError
	accountID  string
	retryAfter string
}

func withRateLimitRequest(ctx context.Context, accountID string) context.Context {
	return context.WithValue(ctx, rateLimitContextKey{}, &requestRateLimit{accountID: accountID})
}

func requestRateLimitFrom(ctx context.Context) *requestRateLimit {
	state, _ := ctx.Value(rateLimitContextKey{}).(*requestRateLimit)
	return state
}

func rememberRateLimit(ctx context.Context, signal *bpsRateLimitError) {
	if state := requestRateLimitFrom(ctx); state != nil && signal != nil {
		state.mu.Lock()
		defer state.mu.Unlock()
		state.signal = signal
	}
}

func observedRateLimit(ctx context.Context) *bpsRateLimitError {
	if state := requestRateLimitFrom(ctx); state != nil {
		state.mu.Lock()
		defer state.mu.Unlock()
		return state.signal
	}
	return nil
}

func (g *Gateway) recordRateLimit(ctx context.Context, accountID, retryAfter string) *bpsRateLimitError {
	signal := g.rateLimits.window(accountID, retryAfter, true)
	rememberRateLimit(ctx, signal)
	return signal
}

func (g *Gateway) coolingAccount(ctx context.Context, accountID string) *bpsRateLimitError {
	signal := g.rateLimits.window(accountID, "", false)
	rememberRateLimit(ctx, signal)
	return signal
}

// SetRateLimits 在开始服务前调用，保留同一引擎进程内的停止/启动冷却状态。
func (g *Gateway) SetRateLimits(limits *RateLimits) {
	if limits != nil {
		g.rateLimits = limits
	}
}
