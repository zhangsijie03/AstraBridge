package gateway

import (
	"bytes"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

const excelBPSInvalidCiphertext = `{"error":{"code":"invalid_encrypted_content","message":"private upstream diagnostic"}}`
const excelBPSInvalidCiphertextNoCode = `{"error":{"message":"The encrypted content fixture...end could not be verified. Reason: Encrypted content could not be decrypted or parsed."}}`

func TestExcelBPSInvalidEncryptedContentClassification(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		want      bool
	}{
		{"code", excelBPSInvalidCiphertext, true},
		{"diagnostic without code", excelBPSInvalidCiphertextNoCode, true},
		{"unrelated code", `{"error":{"code":"invalid_value","message":"The encrypted content could not be verified and could not be decrypted or parsed"}}`, false},
		{"echoed text", `{"error":{"message":"Invalid input. User said: the encrypted content could not be verified and could not be decrypted or parsed"}}`, false},
		{"unrelated request", `{"error":{"message":"Unsupported parameter: encrypted_content"}}`, false},
		{"invalid JSON", `{"error":{"code":"invalid_encrypted_content"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, isExcelBPSInvalidEncryptedContent([]byte(tc.raw)))
		})
	}
}

func TestExcelBPSInvalidEncryptedRetryPreservesHistory(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","reasoning_effort":"xhigh","metadata":{"task_id":"task","turn_id":"turn","agent_iteration":"2"},"prompt_cache_key":"cache","context_management":[{"type":"compaction","compact_threshold":920000}],"input":[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"keep task"}]},
		{"type":"reasoning","id":"rs_opaque","encrypted_content":"private-ciphertext","summary":[]},
		{"type":"function_call","call_id":"call_1","name":"run_officejs","arguments":"{\"value\":9007199254740993}"},
		{"type":"function_call_output","call_id":"call_1","output":"keep result"},
		{"type":"reasoning","summary":[{"type":"summary_text","text":"visible summary"}]},
		{"type":"message","role":"user","content":[{"type":"input_image","file_id":"attachment_1"}]},
		{"type":"compaction_trigger"}
	]}`)
	original := bytes.Clone(body)
	retry, changed := prepareExcelBPSInvalidEncryptedRetry(body, []byte(excelBPSInvalidCiphertext))
	require.True(t, changed)
	require.Equal(t, original, body, "do not mutate the canonical request")
	require.NotContains(t, string(retry), "private-ciphertext")
	require.NotContains(t, string(retry), "rs_opaque")
	require.Contains(t, string(retry), "9007199254740993")
	var before, after map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &before))
	require.NoError(t, json.Unmarshal(retry, &after))
	var input []json.RawMessage
	require.NoError(t, json.Unmarshal(before["input"], &input))
	input = append(input[:1], input[2:]...)
	expected, err := json.Marshal(input)
	require.NoError(t, err)
	require.JSONEq(t, string(expected), string(after["input"]))
	delete(before, "input")
	delete(after, "input")
	require.Equal(t, before, after, "model and request identity must not change")
}

func TestExcelBPSInvalidEncryptedRetryNeverDropsRequiredContext(t *testing.T) {
	for _, item := range []string{
		`{"type":"compaction","encrypted_content":"only-copy-of-context"}`,
		`{"type":"compaction_summary","encrypted_content":"only-copy-of-context"}`,
		`{"type":"agent_message","content":[{"type":"encrypted_content","encrypted_content":"private-message"}]}`,
		`{"type":"function_call_output","output":[{"type":"encrypted_content","encrypted_content":"private-result"}]}`,
		`{"type":"function_call","encrypted_function_args":["message"],"arguments":"{}"}`,
	} {
		body := []byte(`{"input":[{"type":"reasoning","encrypted_content":"opaque"},` + item + `]}`)
		retry, changed := prepareExcelBPSInvalidEncryptedRetry(body, []byte(excelBPSInvalidCiphertext))
		require.False(t, changed)
		require.Equal(t, body, retry)
	}
	for _, body := range []string{
		`{"input":"hello"}`,
		`{"input":[{"type":"reasoning","encrypted_content":"opaque"}]}`,
		`{"input":[{"role":"developer","content":"injected protocol"},{"type":"reasoning","encrypted_content":"opaque"},{"type":"compaction_trigger"}]}`,
		`{"input":[{"role":"user","content":"hello"}]}`,
		`{`,
	} {
		retry, changed := prepareExcelBPSInvalidEncryptedRetry([]byte(body), []byte(excelBPSInvalidCiphertext))
		require.False(t, changed)
		require.Equal(t, body, string(retry))
	}
}
