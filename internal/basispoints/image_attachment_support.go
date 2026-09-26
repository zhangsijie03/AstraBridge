package basispoints

// Extracted from Sub2API 26b324b44c80929e5f86aeb36e09996423c4a5c0
// image_relay.go: only the shared native-upload validators and codec registration.
// The public image-serving and temporary disk-storage feature is not included.
import (
	"encoding/base64"
	"fmt"
	_ "golang.org/x/image/webp"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"mime"
	"strings"
)

const (
	imageRelayMaxImageBytes    = 20 << 20
	imageRelayMaxRequestBytes  = 32 << 20
	imageRelayMaxRequestImages = 20
	imageRelayMaxPixels        = 64 * 1024 * 1024
)

func relayImagePayload(raw string, maxImageMiB int) (string, string, error) {
	header, payload, ok := strings.Cut(raw[len("data:"):], ",")
	if !ok || !strings.HasSuffix(strings.ToLower(header), ";base64") {
		return "", "", fmt.Errorf("basispoints inline image requires a base64 image data URL")
	}
	declared, params, err := mime.ParseMediaType(header[:len(header)-len(";base64")])
	if err != nil || len(params) != 0 {
		return "", "", fmt.Errorf("basispoints inline image has an invalid media type")
	}
	switch declared {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		return "", "", fmt.Errorf("basispoints inline images must be PNG, JPEG, GIF or WebP")
	}
	if len(payload) > base64.StdEncoding.EncodedLen(maxImageMiB<<20) {
		return "", "", fmt.Errorf("image relay inline image exceeds the configured %d MiB limit", maxImageMiB)
	}
	if payload == "" {
		return "", "", fmt.Errorf("basispoints inline image contains invalid base64 data")
	}
	return declared, payload, nil
}
