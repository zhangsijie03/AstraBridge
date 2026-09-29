package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
)

const turnMetadataKey = "x-codex-turn-metadata"
const subagentKey = "x-openai-subagent"

var sessionHeaders = [...]string{"session-id", "session_id", "conversation_id", "X-Session-Affinity", "X-Session-Id", "X-OpenCode-Session", "X-Conversation-ID"}

type turnMetadata struct {
	ThreadID    string `json:"thread_id"`
	RequestKind string `json:"request_kind"`
}

func parseTurnMetadata(raw string) (turnMetadata, bool) {
	var metadata turnMetadata
	if strings.TrimSpace(raw) == "" || json.Unmarshal([]byte(raw), &metadata) != nil {
		return metadata, false
	}
	metadata.ThreadID = strings.TrimSpace(metadata.ThreadID)
	metadata.RequestKind = strings.ToLower(strings.TrimSpace(metadata.RequestKind))
	return metadata, true
}
func rawString(raw json.RawMessage) string {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

// 移植 v2.9.3 openai_ws_execution_scope.go 的身份优先级和执行道隔离。
// 本地仅有一个 API Key，使用其摘要及账号代替平台数据库 ID；不根据聊天内容推断身份。
func requestScope(r *http.Request, source map[string]json.RawMessage, account string) string {
	var client map[string]json.RawMessage
	_ = json.Unmarshal(source["client_metadata"], &client)
	headerMetadata, _ := parseTurnMetadata(r.Header.Get(turnMetadataKey))
	bodyMetadata, _ := parseTurnMetadata(rawString(client[turnMetadataKey]))
	thread := strings.TrimSpace(r.Header.Get("thread-id"))
	if thread == "" {
		thread = headerMetadata.ThreadID
	}
	if thread == "" {
		thread = strings.TrimSpace(strings.SplitN(r.Header.Get("x-codex-window-id"), ":", 2)[0])
	}
	if thread == "" {
		thread = rawString(client["thread_id"])
	}
	if thread == "" {
		thread = bodyMetadata.ThreadID
	}
	metadata, ok := parseTurnMetadata(r.Header.Get(turnMetadataKey))
	if !ok {
		metadata = bodyMetadata
	}
	lane := ""
	switch metadata.RequestKind {
	case "", "turn", "prewarm", "compaction":
	default:
		lane = "kind=" + metadata.RequestKind
	}
	if lane == "" && metadata.ThreadID == "" {
		subagent := strings.TrimSpace(r.Header.Get(subagentKey))
		if subagent == "" {
			subagent = rawString(client[subagentKey])
		}
		if subagent != "" {
			lane = "subagent=" + strings.ToLower(subagent)
		}
	}
	identity := []string{"thread", thread}
	if thread == "" {
		session := ""
		for _, header := range sessionHeaders {
			if session = strings.TrimSpace(r.Header.Get(header)); session != "" {
				break
			}
		}
		if session == "" {
			session = rawString(source["prompt_cache_key"])
		}
		if session == "" {
			return ""
		}
		identity = []string{"session", session}
	}
	// JSON 元组避免客户端标识内分隔符导致键碰撞；摘要不暴露本地 Key。
	seed, _ := json.Marshal([]string{account, r.Header.Get("Authorization"), identity[0], identity[1], lane})
	sum := sha256.Sum256(seed)
	return hex.EncodeToString(sum[:])
}
