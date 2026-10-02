package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func localCitationForTest(t *testing.T) (string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "二期开发计划.md")
	require.NoError(t, os.WriteFile(path, []byte("第一行\n第二行\n"), 0600))
	return path, fmt.Sprintf("【二期开发计划.md:2】 (< %s:2>)", path)
}

func TestRewriteLocalFileCitations(t *testing.T) {
	_, citation := localCitationForTest(t)
	broker := newLocalFileBroker()
	r := newCitationRewriter(broker, "http://127.0.0.1:17861")
	got := r.rewrite("请查看 " + citation + "。")
	require.Contains(t, got, "[二期开发计划.md:2](http://127.0.0.1:17861/v1/files/")
	require.NotContains(t, got, "file://")
	require.NotContains(t, got, "/二期开发计划.md:2>")

	var token string
	for _, issued := range r.links {
		token = issued
		break
	}
	require.NotEmpty(t, token)
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:17861/v1/files/"+token+"#L2", nil)
	resp := httptest.NewRecorder()
	require.True(t, broker.serve(resp, req, token))
	require.Equal(t, http.StatusOK, resp.Code)
	require.Contains(t, resp.Body.String(), "第二行")
	require.Equal(t, "text/markdown; charset=utf-8", resp.Header().Get("Content-Type"))
}

func TestRewriteLocalFileCitationsRejectsSensitivePath(t *testing.T) {
	unsafe := "【密码:1】 (< /Users/zhangsijie/.codex/auth.json:1>)"
	unsafeGot := newCitationRewriter(newLocalFileBroker(), "http://127.0.0.1:17861").rewrite(unsafe)
	require.Contains(t, unsafeGot, "【密码:1】")
	require.Contains(t, unsafeGot, "/Users/zhangsijie/.codex/auth.json:1")
	require.NotContains(t, unsafeGot, "/v1/files/")
}

func TestRewriteLocalFileCitationsRejectsUntrustedFiles(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	for _, path := range []string{
		filepath.Join(home, ".config", "sample", "token.json"),
		filepath.Join(home, ".kube", "config"),
	} {
		got := newCitationRewriter(newLocalFileBroker(), "http://127.0.0.1:17861").rewrite("【文件:1】 (< " + path + ":1>)")
		require.NotContains(t, got, "/v1/files/", path)
	}

	path := filepath.Join(t.TempDir(), "archive.bin")
	require.NoError(t, os.WriteFile(path, []byte("private"), 0600))
	got := newCitationRewriter(newLocalFileBroker(), "http://127.0.0.1:17861").rewrite("【文件:1】 (< " + path + ":1>)")
	require.NotContains(t, got, "/v1/files/")
}

func TestCitationRewriterHandlesSplitDeltas(t *testing.T) {
	path, citation := localCitationForTest(t)
	r := newCitationRewriter(newLocalFileBroker(), "http://127.0.0.1:17861")
	first := r.feed("0:0", "请查看 【二期开发计划.md:2】 (< "+path+":2")
	second := r.feed("0:0", ">)。")
	got := first + second
	require.Contains(t, got, "[二期开发计划.md:2](http://127.0.0.1:17861/v1/files/")
	require.True(t, strings.HasSuffix(got, "。"))
	_ = citation
}

func TestCitationKeepsEarlierOrdinaryBrackets(t *testing.T) {
	_, citation := localCitationForTest(t)
	for _, prefix := range []string{"【注意】请阅读：", "【普通文字未闭合，请阅读："} {
		r := newCitationRewriter(newLocalFileBroker(), "http://127.0.0.1:17861")
		input := prefix + citation + "。"
		require.Equal(t, r.rewrite(input), r.feed("0:0", input), "streamed deltas must match final text")
	}
}

func TestRewriteSSEFrameKeepsProtocolAndRewritesDelta(t *testing.T) {
	_, citation := localCitationForTest(t)
	r := newCitationRewriter(newLocalFileBroker(), "http://127.0.0.1:17861")
	payload, err := json.Marshal(map[string]any{
		"type":          "response.output_text.delta",
		"output_index":  0,
		"content_index": 0,
		"delta":         citation,
	})
	require.NoError(t, err)
	frame := "event: response.output_text.delta\ndata: " + string(payload) + "\n\n"
	got := rewriteSSEFrame(frame, r)
	require.Contains(t, got, "event: response.output_text.delta")
	require.Contains(t, got, "http://127.0.0.1:17861/v1/files/")
	require.NotContains(t, got, "file://")
}

func TestRewriteSSEFrameRewritesCompletedResponse(t *testing.T) {
	_, citation := localCitationForTest(t)
	r := newCitationRewriter(newLocalFileBroker(), "http://127.0.0.1:17861")
	payload, err := json.Marshal(map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"output": []any{map[string]any{
				"type": "message",
				"content": []any{map[string]any{
					"type": "output_text",
					"text": citation,
				}},
			}},
		},
	})
	require.NoError(t, err)
	frame := "event: response.completed\ndata: " + string(payload) + "\n\n"
	got := rewriteSSEFrame(frame, r)
	require.Contains(t, got, "event: response.completed")
	require.Contains(t, got, "http://127.0.0.1:17861/v1/files/")
	require.NotContains(t, got, "file://")
}

func TestLocalFileBrokerRejectsExpiredGrant(t *testing.T) {
	path, _ := localCitationForTest(t)
	broker := newLocalFileBroker()
	token, ok := broker.issue(path)
	require.True(t, ok)
	broker.mu.Lock()
	grant := broker.grants[token]
	grant.expires = grant.expires.Add(-48 * time.Hour)
	broker.grants[token] = grant
	broker.mu.Unlock()
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:17861/v1/files/"+token, nil)
	require.False(t, broker.serve(resp, req, token))
}

func TestGatewayServesOpaqueLocalFileLinkWithoutAPIHeader(t *testing.T) {
	path, _ := localCitationForTest(t)
	g := New("test-key", "gpt-6-astra", nil, nil)
	token, ok := g.fileBroker.issue(path)
	require.True(t, ok)

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:17861/v1/files/"+token, nil)
	resp := httptest.NewRecorder()
	g.ServeHTTP(resp, req)
	require.Equal(t, http.StatusOK, resp.Code)
	require.Contains(t, resp.Body.String(), "第二行")

	for _, host := range []string{"localhost:17861", "192.168.1.20:17861"} {
		blocked := httptest.NewRequest(http.MethodGet, "http://"+host+"/v1/files/"+token, nil)
		blockedResp := httptest.NewRecorder()
		g.ServeHTTP(blockedResp, blocked)
		require.Equal(t, http.StatusForbidden, blockedResp.Code)
	}

	origin := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:17861/v1/files/"+token, nil)
	origin.Header.Set("Origin", "tauri://localhost")
	originResp := httptest.NewRecorder()
	g.ServeHTTP(originResp, origin)
	require.Equal(t, http.StatusForbidden, originResp.Code)
}
