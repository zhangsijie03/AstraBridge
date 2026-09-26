package identity

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func jwt(payload string) string {
	return "header." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".signature"
}
func TestReadsAccountWithoutExposingSecrets(t *testing.T) {
	p := filepath.Join(t.TempDir(), "auth.json")
	token := jwt(`{"exp":4102444800,"https://api.openai.com/auth":{"chatgpt_account_id":"acct-1"},"email":"alice@example.com"}`)
	data := `{"auth_mode":"chatgpt","tokens":{"access_token":"` + token + `","account_id":"acct-1"}}`
	if e := os.WriteFile(p, []byte(data), 0600); e != nil {
		t.Fatal(e)
	}
	a, e := Read(p)
	if e != nil {
		t.Fatal(e)
	}
	if a.AccountID != "acct-1" || a.MaskedEmail != "a***@example.com" {
		t.Fatalf("wrong masked identity: %s", a.MaskedEmail)
	}
}
func TestRejectsExpiredOrAPIKeyCredentials(t *testing.T) {
	for _, data := range []string{`{"auth_mode":"apikey","OPENAI_API_KEY":"secret"}`, `{"auth_mode":"chatgpt","tokens":{"access_token":"` + jwt(`{"exp":1}`) + `","account_id":"a"}}`} {
		p := filepath.Join(t.TempDir(), "auth.json")
		if e := os.WriteFile(p, []byte(data), 0600); e != nil {
			t.Fatal(e)
		}
		if _, e := Read(p); e == nil {
			t.Fatal("invalid credential accepted")
		}
	}
}
