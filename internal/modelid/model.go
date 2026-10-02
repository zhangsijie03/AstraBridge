// Package modelid defines the model IDs that AstraBridge exposes locally.
package modelid

const (
	Default  = "gpt-6-astra"
	GPT61Sol = "gpt-6.1-sol"
)

// IDs returns a fresh slice so callers cannot mutate the supported-model list.
func IDs() []string { return []string{Default, GPT61Sol} }

// IsSupported reports whether model is an explicitly supported local route.
func IsSupported(model string) bool {
	return model == Default || model == GPT61Sol
}
