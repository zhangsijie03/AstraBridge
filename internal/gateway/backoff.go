package gateway

import (
	"sync"
	"time"
)

const (
	upstreamBackoffBase = 2 * time.Second
	upstreamBackoffMax  = 30 * time.Second
)

// upstreamBackoff limits rapid client reconnects while the proxy or BPS route
// is recovering; a successful upstream response clears the window.
type upstreamBackoff struct {
	mu       sync.Mutex
	now      func() time.Time
	failures int
	until    time.Time
}

func newUpstreamBackoff() *upstreamBackoff {
	return &upstreamBackoff{now: time.Now}
}

func (b *upstreamBackoff) remaining() time.Duration {
	if b == nil {
		return 0
	}
	now := b.now()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.until.IsZero() || !now.Before(b.until) {
		return 0
	}
	return b.until.Sub(now)
}

func (b *upstreamBackoff) failure() time.Duration {
	if b == nil {
		return 0
	}
	now := b.now()
	b.mu.Lock()
	defer b.mu.Unlock()
	if now.Before(b.until) {
		return b.until.Sub(now)
	}
	b.failures++
	delay := upstreamBackoffBase
	for i := 1; i < b.failures && delay < upstreamBackoffMax; i++ {
		delay *= 2
	}
	if delay > upstreamBackoffMax {
		delay = upstreamBackoffMax
	}
	b.until = now.Add(delay)
	return delay
}

func (b *upstreamBackoff) success() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures = 0
	b.until = time.Time{}
}

func backoffSeconds(delay time.Duration) int {
	if delay <= 0 {
		return 0
	}
	seconds := int((delay + time.Second - 1) / time.Second)
	if seconds < 1 {
		return 1
	}
	return seconds
}
