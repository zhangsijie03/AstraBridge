package basispoints

import (
	"fmt"
	"net/url"
	"strings"
)

// Accept HTTPS URLs or validated native attachment references.
func validateImage(part object) error {
	if fileID, exists := part["file_id"]; exists {
		id, ok := fileID.(string)
		if !ok || !ValidAttachmentID(id) {
			return fmt.Errorf("basispoints input_image requires a valid file_id")
		}
		if _, exists := part["image_url"]; exists {
			return fmt.Errorf("basispoints input_image requires exactly one image reference")
		}
	} else {
		raw, ok := part["image_url"].(string)
		if !ok || raw == "" {
			return fmt.Errorf("basispoints input_image requires an HTTPS image_url or file_id")
		}
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(raw)), "data:") {
			return fmt.Errorf("basispoints does not accept data:image input while image support is disabled; ask an administrator to enable BPS image support, provide an HTTPS image URL, or disable Basispoints and start a new conversation")
		}
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Opaque != "" || strings.TrimSpace(raw) != raw {
			return fmt.Errorf("basispoints input_image requires an absolute HTTPS image URL without embedded credentials")
		}
	}
	return validateImageDetail(part)
}

func validateImageDetail(part object) error {
	if detail, exists := part["detail"]; exists && detail != nil {
		switch text(detail) {
		case "auto", "low", "high", "original":
		default:
			return fmt.Errorf("basispoints image detail must be auto, low, high or original")
		}
	}
	return nil
}

// Normalize only validated, translated messages. This also covers file images
// moved out of tool results; inline tool screenshots and HTTPS URLs keep their
// own contract. Running after validation must not hide malformed references.
func normalizeMessageFileImages(input []any) {
	for _, raw := range input {
		item, _ := raw.(object)
		kind := text(item["type"])
		if kind != "message" && (kind != "" || text(item["role"]) == "") {
			continue
		}
		parts, _ := item["content"].([]any)
		for i, rawPart := range parts {
			part, _ := rawPart.(object)
			if text(part["type"]) == "input_image" && text(part["file_id"]) != "" {
				parts[i] = object{"type": "input_image", "file_id": part["file_id"]}
			}
		}
	}
}
