package gateway

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var localFileCitationPattern = regexp.MustCompile("【([^】:\\r\\n]+):(\\d+)】\\s*\\(\\s*<\\s*([^>\\r\\n]+)\\s*>\\s*\\)")

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
	path    string
	expires time.Time
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
	if b == nil || !safeLocalCitationPath(path) {
		return "", false
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > localFileMaxBytes {
		return "", false
	}
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
	b.grants[token] = localFileGrant{path: path, expires: now.Add(localFileGrantTTL)}
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
	file, err := os.Open(grant.path)
	if err != nil {
		return false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > localFileMaxBytes {
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
	pending map[string]string
	links   map[string]string
	broker  *localFileBroker
	baseURL string
}

func newCitationRewriter(broker *localFileBroker, baseURL string) *citationRewriter {
	return &citationRewriter{pending: make(map[string]string), links: make(map[string]string), broker: broker, baseURL: strings.TrimRight(baseURL, "/")}
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
		if strings.IndexRune(candidate, '】') < 0 || citationMayContinue(candidate) {
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
	close := strings.IndexRune(value, '】')
	if close < 0 {
		return true
	}
	rest := strings.TrimSpace(value[close+len("】"):])
	return rest == "" || strings.HasPrefix(rest, "(") || strings.HasPrefix(rest, "(<")
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
		rewriteSSEPayload(payload, rewriter)
		encoded, err := json.Marshal(payload)
		if err == nil {
			lines[i] = "data: " + string(encoded) + "\n"
		}
	}
	return strings.Join(lines, "")
}

func rewriteSSEPayload(payload map[string]any, rewriter *citationRewriter) {
	kind, _ := payload["type"].(string)
	switch kind {
	case "response.output_text.delta":
		if delta, ok := payload["delta"].(string); ok {
			payload["delta"] = rewriter.feed(citationKey(payload), delta)
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
