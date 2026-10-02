package gateway

import (
	"bpslocal/internal/identity"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRelayAcceptsGPT61SolAndPreservesModel(t *testing.T) {
	g := gateway(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model != "gpt-6.1-sol" {
			t.Fatalf("6.1 Sol model was changed: %+v, %v", body, err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"sol-1\",\"status\":\"completed\",\"model\":\"gpt-6.1-sol\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}]}}\n\n")
	})
	w := httptest.NewRecorder()
	g.model = "gpt-6.1-sol"
	g.ServeHTTP(w, request(`{"model":"gpt-6.1-sol","input":"hello","stream":true,"reasoning":{"effort":"max"}}`))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "sol-1") {
		t.Fatalf("6.1 Sol request failed: %d %s", w.Code, w.Body.String())
	}
}

// 客户端只提供本地 Key；上游身份必须取自服务端，不能被客户端账号头覆盖。
func TestRelayUsesServerAccountWithOnlyAPIKey(t *testing.T) {
	g := gateway(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token() || r.Header.Get("Chatgpt-Account-Id") != "account-1" {
			t.Error("client controlled upstream identity")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, completed)
	})
	r := request(simpleRequest)
	r.Header.Del(KeyHeader)
	r.Header.Set("Authorization", "Bearer local-key")
	r.Header.Set("Chatgpt-Account-Id", "untrusted-client-account")
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "response.completed") {
		t.Fatal("API Key-only relay rejected", w.Code, w.Body.String())
	}
}
func TestRelayModelsNeedsOnlyLocalKey(t *testing.T) {
	g := gateway(t, func(w http.ResponseWriter, r *http.Request) { t.Error("model listing must not contact upstream") })
	r := request("")
	r.Method = "GET"
	r.URL.Path = "/v1/models"
	r.Header.Del(KeyHeader)
	r.Header.Set("Authorization", "Bearer local-key")
	r.Header.Del("Chatgpt-Account-Id")
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"gpt-6-astra"`) {
		t.Fatal("cannot list configured model", w.Code, w.Body.String())
	}
}

func TestRelayModelsListsBothSupportedRoutesWhenUnpinned(t *testing.T) {
	g := New("local-key", "", nil, nil)
	r := request("")
	r.Method = "GET"
	r.URL.Path = "/v1/models"
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"id":"gpt-6-astra"`) || !strings.Contains(w.Body.String(), `"id":"gpt-6.1-sol"`) {
		t.Fatalf("supported model list incomplete: %d %s", w.Code, w.Body.String())
	}
}

func TestRelayRejectsModelOutsideFixedBPSModel(t *testing.T) {
	called := false
	g := gateway(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
	})
	w := httptest.NewRecorder()
	g.ServeHTTP(w, request(`{"model":"gpt-5.6-sol","input":"hello","stream":true}`))
	if w.Code != http.StatusBadRequest || called || !strings.Contains(w.Body.String(), "unsupported_model") {
		t.Fatalf("unsupported model reached upstream or wrong response: %d %s", w.Code, w.Body.String())
	}
}

func TestRelayReloadsAccountWithoutExposingKeys(t *testing.T) {
	var received []string
	g := gateway(t, func(w http.ResponseWriter, r *http.Request) {
		received = append(received, r.Header.Get("Chatgpt-Account-Id"))
		if r.Header.Get("Authorization") == "Bearer local-key" {
			t.Error("local API Key leaked upstream")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, completed)
	})
	account, _ := testAccount()
	g.accountSource = func() (identity.Account, error) { return account, nil }
	for _, id := range []string{"account-1", "account-2"} {
		account.AccountID = id
		w := httptest.NewRecorder()
		g.ServeHTTP(w, request(simpleRequest))
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), account.AccessToken) {
			t.Fatal("credential leaked to client")
		}
	}
	if len(received) != 2 || received[0] != "account-1" || received[1] != "account-2" {
		t.Fatal("upstream identity cached across switch", received)
	}
}
