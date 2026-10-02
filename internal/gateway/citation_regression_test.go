package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func citationSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("Windows symlink privilege unavailable: %v", err)
		}
		t.Fatal(err)
	}
}

func TestLocalFileGrantRejectsReplacements(t *testing.T) {
	for _, replacement := range []string{"file", "symlink", "parent-symlink", "sensitive-symlink"} {
		t.Run(replacement, func(t *testing.T) {
			root := t.TempDir()
			directory := filepath.Join(root, "reports")
			require.NoError(t, os.Mkdir(directory, 0700))
			path := filepath.Join(directory, "report.txt")
			require.NoError(t, os.WriteFile(path, []byte("public report"), 0600))
			g := New("test-key", "gpt-6-astra", nil, nil)
			token, ok := g.fileBroker.issue(path)
			require.True(t, ok)
			// 保留旧 inode，保证测试验证身份变化而不是依赖文件系统分配时机。
			if replacement == "parent-symlink" {
				original := filepath.Join(root, "original")
				require.NoError(t, os.Rename(directory, original))
				citationSymlink(t, original, directory)
			} else {
				require.NoError(t, os.Rename(path, path+".saved"))
				switch replacement {
				case "file":
					require.NoError(t, os.WriteFile(path, []byte("replacement"), 0600))
				case "symlink":
					citationSymlink(t, path+".saved", path)
				case "sensitive-symlink":
					secret := filepath.Join(root, ".codex", "auth.json")
					require.NoError(t, os.MkdirAll(filepath.Dir(secret), 0700))
					require.NoError(t, os.WriteFile(secret, []byte("SYNTHETIC_SECRET"), 0600))
					citationSymlink(t, secret, path)
					require.False(t, safeLocalCitationPath(path))
					_, issued := g.fileBroker.issue(path)
					require.False(t, issued)
				}
			}
			response := httptest.NewRecorder()
			g.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:17861/v1/files/"+token, nil))
			require.Equal(t, http.StatusNotFound, response.Code)
			require.NotContains(t, response.Body.String(), "SYNTHETIC_SECRET")
			require.NotContains(t, response.Body.String(), "replacement")
		})
	}
}

func TestLocalFileGrantPreservesHeadAndRange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.txt")
	require.NoError(t, os.WriteFile(path, []byte("public report"), 0600))
	broker := newLocalFileBroker()
	token, ok := broker.issue(path)
	require.True(t, ok)
	for _, method := range []string{http.MethodHead, http.MethodGet} {
		req := httptest.NewRequest(method, "/v1/files/"+token, nil)
		if method == http.MethodGet {
			req.Header.Set("Range", "bytes=0-5")
		}
		response := httptest.NewRecorder()
		require.True(t, broker.serve(response, req, token))
		if method == http.MethodHead {
			require.Equal(t, http.StatusOK, response.Code)
			require.Empty(t, response.Body.String())
		} else {
			require.Equal(t, http.StatusPartialContent, response.Code)
			require.Equal(t, "public", response.Body.String())
		}
	}
}

func citationEventFrame(t *testing.T, event map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(event)
	require.NoError(t, err)
	return fmt.Sprintf("event: %s\ndata: %s\n\n", event["type"], raw)
}

func citationEvents(t *testing.T, frames string) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, line := range strings.Split(frames, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event map[string]any
		require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event))
		events = append(events, event)
	}
	return events
}

func TestCitationStreamPreservesTextAtEverySplit(t *testing.T) {
	_, citation := localCitationForTest(t)
	formatted := strings.NewReplacer("】 (", "】\n(\n", "< ", "<\n", ">)", "\n>\n)").Replace(citation)
	for _, value := range []string{"请看这里【注意】", "开始【注意】(仅供参考)后续正文", "文本【未闭合的普通内容", "末尾【", "【文件:1】 (<尚未结束", "【嵌套【注意】正文", "请读" + citation + "。", formatted} {
		t.Run(value, func(t *testing.T) {
			runes := []rune(value)
			for split := 0; split <= len(runes); split++ {
				r := newCitationRewriter(newLocalFileBroker(), "http://127.0.0.1:17861")
				var output strings.Builder
				for index, chunk := range []string{string(runes[:split]), string(runes[split:])} {
					output.WriteString(rewriteSSEFrame(citationEventFrame(t, map[string]any{"type": "response.output_text.delta", "sequence_number": index, "item_id": "message-0", "output_index": 0, "content_index": 0, "delta": chunk}), r))
				}
				output.WriteString(rewriteSSEFrame(citationEventFrame(t, map[string]any{"type": "response.output_text.done", "sequence_number": 2, "text": value}), r))
				output.WriteString(rewriteSSEFrame(citationEventFrame(t, map[string]any{"type": "response.completed", "sequence_number": 3}), r))
				var streamed strings.Builder
				for index, event := range citationEvents(t, output.String()) {
					require.Equal(t, float64(index), event["sequence_number"], "split %d", split)
					if event["type"] == "response.output_text.delta" {
						streamed.WriteString(event["delta"].(string))
						require.Equal(t, "message-0", event["item_id"])
					}
					if event["type"] == "response.output_text.done" {
						require.Equal(t, r.rewrite(value), event["text"])
					}
				}
				require.Equal(t, r.rewrite(value), streamed.String(), "split %d", split)
				require.Empty(t, r.pending)
				require.Empty(t, r.pendingEvents)
			}
		})
	}
}

func TestCitationPendingFlushesBeforeTerminal(t *testing.T) {
	for _, terminal := range []string{"response.completed", "response.failed", "response.cancelled", "response.incomplete", "error"} {
		t.Run(terminal, func(t *testing.T) {
			r := newCitationRewriter(nil, "")
			for index := 0; index < 2; index++ {
				rewriteSSEFrame(citationEventFrame(t, map[string]any{"type": "response.output_text.delta", "sequence_number": index, "item_id": fmt.Sprint(index), "output_index": index, "content_index": 1, "delta": "【尾部"}), r)
			}
			events := citationEvents(t, rewriteSSEFrame(citationEventFrame(t, map[string]any{"type": terminal, "sequence_number": 2}), r))
			require.Len(t, events, 3)
			for index, event := range events[:2] {
				require.Equal(t, "response.output_text.delta", event["type"])
				require.Equal(t, "【尾部", event["delta"])
				require.Equal(t, float64(index), event["output_index"])
				require.Equal(t, float64(1), event["content_index"])
				require.Equal(t, fmt.Sprint(index), event["item_id"])
				require.Equal(t, float64(index+2), event["sequence_number"])
			}
			require.Equal(t, terminal, events[2]["type"])
			require.Equal(t, float64(4), events[2]["sequence_number"])
			require.Empty(t, r.pending)
		})
	}
}

func TestCitationDoneFlushesOnlyMatchingOutput(t *testing.T) {
	for _, done := range []string{"response.output_text.done", "response.content_part.done", "response.output_item.done"} {
		t.Run(done, func(t *testing.T) {
			r := newCitationRewriter(nil, "")
			for index := 0; index < 2; index++ {
				rewriteSSEFrame(citationEventFrame(t, map[string]any{"type": "response.output_text.delta", "output_index": index, "content_index": 2, "delta": "【尾部"}), r)
			}
			doneFrame := citationEventFrame(t, map[string]any{"type": done, "output_index": 1, "content_index": 2})
			events := citationEvents(t, rewriteSSEFrame(doneFrame, r))
			require.Len(t, events, 2)
			require.Equal(t, float64(1), events[0]["output_index"])
			require.Equal(t, map[string]string{"0:2": "【尾部"}, r.pending)
			require.Len(t, citationEvents(t, rewriteSSEFrame(doneFrame, r)), 1, "must not flush twice")
		})
	}
}

func TestCitationTextThroughFullGateway(t *testing.T) {
	for _, complete := range []bool{true, false} {
		value := "开始【注意】(仅供参考)后续正文【未闭合"
		g := gateway(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, citationEventFrame(t, map[string]any{"type": "response.output_text.delta", "sequence_number": 0, "delta": value}))
			if complete {
				fmt.Fprint(w, citationEventFrame(t, map[string]any{"type": "response.completed", "sequence_number": 1, "response": map[string]any{"status": "completed", "output": []any{}}}))
			}
		})
		response := httptest.NewRecorder()
		g.ServeHTTP(response, request(simpleRequest))
		require.Equal(t, http.StatusOK, response.Code)
		events := citationEvents(t, response.Body.String())
		var streamed strings.Builder
		sequence := 0
		for _, event := range events {
			if event["type"] != "keepalive" {
				require.Equal(t, float64(sequence), event["sequence_number"])
				sequence++
			}
			if event["type"] == "response.output_text.delta" {
				streamed.WriteString(event["delta"].(string))
			}
		}
		require.Equal(t, value, streamed.String())
		if complete {
			require.Equal(t, "response.completed", events[len(events)-1]["type"])
		} else {
			require.Equal(t, "response.failed", events[len(events)-1]["type"])
		}
	}
}
