package modelid

import "testing"

func TestSupportedModels(t *testing.T) {
	for _, id := range []string{Default, GPT61Sol} {
		if !IsSupported(id) {
			t.Fatalf("supported model rejected: %s", id)
		}
	}
	if IsSupported("gpt-5.6-sol") || IsSupported("") {
		t.Fatal("unsupported model accepted")
	}
	ids := IDs()
	ids[0] = "changed"
	if IDs()[0] != Default {
		t.Fatal("model list must not be mutable by callers")
	}
}
