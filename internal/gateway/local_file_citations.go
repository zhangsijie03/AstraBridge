package gateway

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var localFileCitationPattern = regexp.MustCompile("【([^【】:\\r\\n]+):(\\d+)】\\s*\\(\\s*<\\s*([^>\\r\\n]+)\\s*>\\s*\\)")

const (
	localFileGrantTTL = 24 * time.Hour
	localFileGrantMax = 4096
	localFileMaxBytes = 32 << 20
)

var localFileSafeExtensions = map[string]struct{}{
	".md": {}, ".txt": {}, ".log": {}, ".json": {}, ".yaml": {}, ".yml": {}, ".toml": {},
	".xml": {}, ".html": {}, ".htm": {}, ".css": {}, ".js": {}, ".ts": {}, ".tsx": {},
	".jsx": {}, ".go": {}, ".java": {}, ".kt": {}, ".py": {}, ".rs": {}, ".swift": {},
	".cs": {}, ".cpp": {}, ".c": {}, ".h": {}, ".sql": {}, ".sh": {}, ".zsh": {},
	".ps1": {}, ".bat": {}, ".csv": {}, ".diff": {}, ".patch": {}, ".png": {},
	".jpg": {}, ".jpeg": {}, ".gif": {}, ".webp": {}, ".pdf": {},
}

var localFileSensitiveSegments = map[string]struct{}{
	".ssh": {}, ".aws": {}, ".codex": {}, ".config": {}, ".kube": {}, ".gnupg": {},
	".npm": {}, ".docker": {}, ".git": {}, "keychains": {},
}

type localFileGrant struct {
	path     string
	identity os.FileInfo
	expires  time.Time
}

// localFileBroker turns a validated local path into a short-lived opaque URL.
// The URL is intentionally independent from the API key because AiMaMi opens
// links through the system browser and cannot attach Authorization headers.
type localFileBroker struct {
	mu     sync.Mutex
	grants map[string]localFileGrant
}

func newLocalFileBroker() *localFileBroker {
	return &localFileBroker{grants: make(map[string]localFileGrant)}
}

func (b *localFileBroker) issue(path string) (string, bool) {
	canonical, ok := canonicalLocalCitationPath(path)
	if b == nil || !ok {
		return "", false
	}
	file, info, err := openLocalCitation(canonical)
	if err != nil {
		return "", false
	}
	_ = file.Close()
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", false
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	now := time.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	for len(b.grants) >= localFileGrantMax {
		var oldest string
		var oldestAt time.Time
		for key, grant := range b.grants {
			if grant.expires.Before(now) || oldest == "" || grant.expires.Before(oldestAt) {
				oldest, oldestAt = key, grant.expires
			}
		}
		delete(b.grants, oldest)
	}
	b.grants[token] = localFileGrant{path: canonical, identity: info, expires: now.Add(localFileGrantTTL)}
	return token, true
}

func (b *localFileBroker) serve(w http.ResponseWriter, r *http.Request, token string) bool {
	if b == nil || (r.Method != http.MethodGet && r.Method != http.MethodHead) || token == "" {
		return false
	}
	now := time.Now()
	b.mu.Lock()
	grant, ok := b.grants[token]
	if ok && !grant.expires.After(now) {
		delete(b.grants, token)
		ok = false
	}
	b.mu.Unlock()
	if !ok || !safeLocalCitationPath(grant.path) {
		return false
	}
	file, info, err := openLocalCitation(grant.path)
	if err != nil {
		return false
	}
	defer file.Close()
	if !os.SameFile(grant.identity, info) {
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", "inline; filename*=UTF-8''"+url.PathEscape(filepath.Base(grant.path)))
	w.Header().Set("Content-Type", localFileContentType(grant.path))
	http.ServeContent(w, r, filepath.Base(grant.path), info.ModTime(), file)
	return true
}

func localFileContentType(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	textual := map[string]bool{".md": true, ".txt": true, ".log": true, ".json": true, ".yaml": true, ".yml": true, ".toml": true, ".xml": true, ".html": true, ".htm": true, ".css": true, ".js": true, ".ts": true, ".tsx": true, ".jsx": true, ".go": true, ".java": true, ".kt": true, ".py": true, ".rs": true, ".swift": true, ".cs": true, ".cpp": true, ".c": true, ".h": true, ".sql": true, ".sh": true, ".zsh": true, ".ps1": true, ".bat": true, ".csv": true, ".diff": true, ".patch": true}
	if textual[ext] {
		if ext == ".md" {
			return "text/markdown; charset=utf-8"
		}
		return "text/plain; charset=utf-8"
	}
	if contentType := mime.TypeByExtension(ext); contentType != "" {
		return contentType
	}
	return "application/octet-stream"
}

type citationRewriter struct {
	pending        map[string]string
	pendingEvents  map[string]map[string]any
	sequenceOffset int64
	lastSequence   int64
	hasSequence    bool
	links          map[string]string
	broker         *localFileBroker
	baseURL        string
}

func newCitationRewriter(broker *localFileBroker, baseURL string) *citationRewriter {
	return &citationRewriter{pending: make(map[string]string), pendingEvents: make(map[string]map[string]any), links: make(map[string]string), broker: broker, baseURL: strings.TrimRight(baseURL, "/")}
}

func (r *citationRewriter) feed(key, chunk string) string {
	if chunk == "" {
		return chunk
	}
	value := r.pending[key] + chunk
	delete(r.pending, key)
	var out strings.Builder
	for value != "" {
		start := strings.IndexRune(value, '【')
		if start < 0 {
			out.WriteString(value)
			break
		}
		out.WriteString(value[:start])
		candidate := value[start:]
		match := localFileCitationPattern.FindStringSubmatch(candidate)
		if match != nil {
			// 匹配可能从后面的文件引用开始；先保留普通【提示】文本，
			// 再按真实起点消费字节，防止切断 UTF-8 或吞掉前文。
			matchStart := strings.Index(candidate, match[0])
			out.WriteString(candidate[:matchStart])
			out.WriteString(r.render(match[1], match[2], match[3]))
			value = candidate[matchStart+len(match[0]):]
			continue
		}
		if citationMayContinue(candidate) {
			r.pending[key] = candidate
			break
		}
		out.WriteRune('【')
		value = candidate[len("【"):]
	}
	return out.String()
}

func (r *citationRewriter) finish(key, value string) string {
	delete(r.pending, key)
	return r.rewrite(value)
}

func (r *citationRewriter) rewrite(value string) string {
	return localFileCitationPattern.ReplaceAllStringFunc(value, func(raw string) string {
		match := localFileCitationPattern.FindStringSubmatch(raw)
		if len(match) != 4 {
			return raw
		}
		return r.render(match[1], match[2], match[3])
	})
}

func citationMayContinue(value string) bool {
	// 只缓存真正可能构成引用的前缀；普通【说明】和普通括号立即透传。
	label, rest, colon := strings.Cut(strings.TrimPrefix(value, "【"), ":")
	if strings.ContainsAny(label, "【】\r\n") {
		return false
	}
	if !colon {
		return true
	}
	if label == "" {
		return false
	}
	i := 0
	for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
		i++
	}
	if i == len(rest) {
		return true
	}
	if i == 0 || !strings.HasPrefix(rest[i:], "】") {
		return false
	}
	rest = rest[i+len("】"):]
	for _, delimiter := range []string{"(", "<"} {
		rest = strings.TrimLeft(rest, " \t\r\n\f")
		if rest == "" {
			return true
		}
		if !strings.HasPrefix(rest, delimiter) {
			return false
		}
		rest = strings.TrimPrefix(rest, delimiter)
	}
	rest = strings.TrimLeft(rest, " \t\r\n\f")
	location, suffix, closed := strings.Cut(rest, ">")
	// 路径外围可分行排版，但路径正文中的换行不属于引用语法。
	if strings.ContainsAny(strings.TrimRight(location, " \t\r\n\f"), "\r\n") {
		return false
	}
	if !closed {
		return true
	}
	return strings.TrimSpace(location) != "" && strings.TrimSpace(suffix) == ""
}

func (r *citationRewriter) render(label, labelLine, location string) string {
	path, pathLine, ok := splitCitationLocation(location)
	if !ok {
		return "【" + label + ":" + labelLine + "】 (<" + location + ">)"
	}
	canonical, ok := canonicalLocalCitationPath(path)
	if !ok || r.broker == nil || r.baseURL == "" {
		return "【" + label + ":" + labelLine + "】 (<" + path + ":" + pathLine + ">)"
	}
	token, ok := r.links[canonical]
	if !ok {
		token, ok = r.broker.issue(canonical)
		if !ok {
			return "【" + label + ":" + labelLine + "】 (<" + path + ":" + pathLine + ">)"
		}
		r.links[canonical] = token
	}
	fileURL := r.baseURL + "/v1/files/" + url.PathEscape(token) + "#L" + pathLine
	label = strings.NewReplacer("\\", "\\\\", "[", "\\[", "]", "\\]").Replace(label)
	return "[" + label + ":" + labelLine + "](" + fileURL + ")"
}

func splitCitationLocation(location string) (string, string, bool) {
	index := strings.LastIndex(location, ":")
	if index <= 0 || index == len(location)-1 {
		return "", "", false
	}
	line := location[index+1:]
	if line == "" {
		return "", "", false
	}
	for _, ch := range line {
		if ch < '0' || ch > '9' {
			return "", "", false
		}
	}
	return location[:index], line, true
}

func safeLocalCitationPath(raw string) bool {
	path := filepath.Clean(strings.TrimSpace(raw))
	if !filepath.IsAbs(path) {
		return false
	}
	resolved := path
	if candidate, err := filepath.EvalSymlinks(path); err == nil {
		resolved = filepath.Clean(candidate)
	}
	roots := []string{os.TempDir()}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		roots = append(roots, home)
	}
	allowed := false
	for _, root := range roots {
		if pathWithinLocalRoot(root, resolved) {
			allowed = true
			break
		}
	}
	if !allowed {
		return false
	}
	return safeLocalCitationName(path) && safeLocalCitationName(resolved)
}

func safeLocalCitationName(path string) bool {
	for _, segment := range strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' }) {
		if _, sensitive := localFileSensitiveSegments[strings.ToLower(segment)]; sensitive {
			return false
		}
	}
	lower := strings.ToLower(filepath.Base(path))
	if lower == ".env" || strings.HasPrefix(lower, ".env.") {
		return false
	}
	for _, suffix := range []string{".pem", ".key", ".p12", ".pfx", ".netrc", "auth.json", "credentials", "token.json", "tokens.json", "secrets.json", "id_rsa", "id_ed25519", "known_hosts"} {
		if lower == suffix || strings.HasSuffix(lower, suffix) {
			return false
		}
	}
	if _, safe := localFileSafeExtensions[strings.ToLower(filepath.Ext(path))]; !safe {
		return false
	}
	return true
}

// 使用逐级目录句柄打开已经规范化的路径，不跟随文件或目录的符号链接。
// 每次打开后核对身份，避免检查与打开之间的替换；在完成核对前不读取内容。
func openLocalCitation(path string) (*os.File, os.FileInfo, error) {
	denied := errors.New("local file changed or is not safe")
	if !safeLocalCitationPath(path) {
		return nil, nil, denied
	}
	roots := []string{os.TempDir()}
	if home, err := os.UserHomeDir(); err == nil {
		roots = append(roots, home)
	}
	for _, rootPath := range roots {
		rootPath, err := filepath.EvalSymlinks(rootPath)
		if err != nil || !pathWithinLocalRoot(rootPath, path) {
			continue
		}
		relative, err := filepath.Rel(rootPath, path)
		if err != nil {
			return nil, nil, err
		}
		root, err := os.OpenRoot(rootPath)
		if err != nil {
			return nil, nil, err
		}
		defer func() { _ = root.Close() }()
		parts := strings.Split(relative, string(filepath.Separator))
		for _, part := range parts[:len(parts)-1] {
			info, err := root.Lstat(part)
			if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return nil, nil, denied
			}
			next, err := root.OpenRoot(part)
			if err != nil {
				return nil, nil, err
			}
			opened, err := next.Stat(".")
			if err != nil || !os.SameFile(info, opened) {
				_ = next.Close()
				return nil, nil, denied
			}
			_ = root.Close()
			root = next
		}
		name := parts[len(parts)-1]
		before, err := root.Lstat(name)
		if err != nil || !before.Mode().IsRegular() {
			return nil, nil, denied
		}
		file, err := root.Open(name)
		if err != nil {
			return nil, nil, err
		}
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() > localFileMaxBytes || !os.SameFile(before, info) {
			_ = file.Close()
			return nil, nil, denied
		}
		return file, info, nil
	}
	return nil, nil, denied
}

func pathWithinLocalRoot(root, path string) bool {
	root = filepath.Clean(root)
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = filepath.Clean(resolved)
	}
	rel, err := filepath.Rel(root, filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func canonicalLocalCitationPath(raw string) (string, bool) {
	path := filepath.Clean(strings.TrimSpace(raw))
	if !safeLocalCitationPath(path) {
		return "", false
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", false
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Size() > localFileMaxBytes || !safeLocalCitationPath(resolved) {
		return "", false
	}
	return resolved, true
}

func rewriteLocalFileCitations(value string) string {
	return newCitationRewriter(nil, "").rewrite(value)
}

func rewriteSSEFrame(frame string, rewriter *citationRewriter) string {
	if rewriter == nil {
		return frame
	}
	lines := strings.SplitAfter(frame, "\n")
	var prefix strings.Builder
	for i, line := range lines {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		raw := strings.TrimSuffix(strings.TrimPrefix(line, "data: "), "\n")
		var payload map[string]any
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.UseNumber()
		if decoder.Decode(&payload) != nil || payload == nil {
			continue
		}
		// 在 done/终止事件前补齐缓存正文，并为插入事件调整序号，保持严格客户端兼容。
		for _, delta := range rewriter.flushBefore(payload) {
			if sequence, ok := payload["sequence_number"]; ok {
				delta["sequence_number"] = sequence
			}
			rewriter.adjustSequence(delta)
			encoded, _ := json.Marshal(delta)
			prefix.WriteString("event: response.output_text.delta\ndata: " + string(encoded) + "\n\n")
			rewriter.sequenceOffset++
		}
		rewriteSSEPayload(payload, rewriter)
		rewriter.adjustSequence(payload)
		encoded, err := json.Marshal(payload)
		if err == nil {
			lines[i] = "data: " + string(encoded) + "\n"
		}
	}
	return prefix.String() + strings.Join(lines, "")
}

func (r *citationRewriter) adjustSequence(payload map[string]any) {
	// 本地生成的断流失败事件没有上游序号，也要延续已开始编号的流。
	if value, ok := payload["sequence_number"].(json.Number); ok {
		if number, err := value.Int64(); err == nil {
			r.lastSequence = number + r.sequenceOffset
			r.hasSequence = true
			payload["sequence_number"] = r.lastSequence
		}
	} else if r.hasSequence {
		r.lastSequence++
		payload["sequence_number"] = r.lastSequence
	}
}

func (r *citationRewriter) flushBefore(payload map[string]any) []map[string]any {
	kind, _ := payload["type"].(string)
	var keys []string
	for key := range r.pending {
		flush := false
		switch kind {
		case "response.output_text.done", "response.content_part.done":
			flush = key == citationKey(payload)
		case "response.output_item.done":
			flush = strings.HasPrefix(key, jsonNumberText(payload["output_index"])+":")
		case "response.completed", "response.failed", "response.cancelled", "response.incomplete", "error":
			flush = true
		}
		if flush {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	var deltas []map[string]any
	for _, key := range keys {
		delta := r.pendingEvents[key]
		if delta != nil && r.pending[key] != "" {
			delta["delta"] = r.pending[key]
			delete(delta, "sequence_number")
			deltas = append(deltas, delta)
		}
		delete(r.pending, key)
		delete(r.pendingEvents, key)
	}
	return deltas
}

func rewriteSSEPayload(payload map[string]any, rewriter *citationRewriter) {
	kind, _ := payload["type"].(string)
	switch kind {
	case "response.output_text.delta":
		if delta, ok := payload["delta"].(string); ok {
			payload["delta"] = rewriter.feed(citationKey(payload), delta)
			if rewriter.pending[citationKey(payload)] != "" {
				metadata := make(map[string]any, len(payload))
				for key, value := range payload {
					metadata[key] = value
				}
				rewriter.pendingEvents[citationKey(payload)] = metadata
			} else {
				delete(rewriter.pendingEvents, citationKey(payload))
			}
		}
	case "response.output_text.done":
		if value, ok := payload["text"].(string); ok {
			payload["text"] = rewriter.finish(citationKey(payload), value)
		}
	case "response.content_part.added", "response.content_part.done":
		if part, ok := payload["part"].(map[string]any); ok {
			rewriteOutputTextPart(part, rewriter)
		}
	case "response.output_item.added", "response.output_item.done":
		if item, ok := payload["item"].(map[string]any); ok {
			rewriteOutputTextItem(item, rewriter)
		}
	case "response.completed":
		if response, ok := payload["response"].(map[string]any); ok {
			if output, ok := response["output"].([]any); ok {
				for _, raw := range output {
					if item, ok := raw.(map[string]any); ok {
						rewriteOutputTextItem(item, rewriter)
					}
				}
			}
		}
	}
}

func citationKey(payload map[string]any) string {
	return jsonNumberText(payload["output_index"]) + ":" + jsonNumberText(payload["content_index"])
}

func jsonNumberText(value any) string {
	switch v := value.(type) {
	case json.Number:
		return v.String()
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return "0"
	}
}

func rewriteOutputTextItem(item map[string]any, rewriter *citationRewriter) {
	if item["type"] != "message" {
		return
	}
	content, _ := item["content"].([]any)
	for _, raw := range content {
		if part, ok := raw.(map[string]any); ok {
			rewriteOutputTextPart(part, rewriter)
		}
	}
}

func rewriteOutputTextPart(part map[string]any, rewriter *citationRewriter) {
	if part["type"] != "output_text" {
		return
	}
	if value, ok := part["text"].(string); ok {
		part["text"] = rewriter.rewrite(value)
	}
}
