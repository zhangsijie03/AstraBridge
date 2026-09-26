package basispoints

import (
	"container/list"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

type tool struct {
	Name       string
	Namespace  string
	Kind       string
	Definition string
	Parameters object
}

type replayEntry struct {
	key             string
	raw             []byte
	callFingerprint string
}

// ReplayCache retains native tool identities without mixing accounts or sessions.
// Both entry count and bytes are bounded because tool arguments can be large.
type ReplayCache struct {
	mu      sync.Mutex
	entries map[string]*list.Element
	order   list.List
	bytes   int
}

func (c *ReplayCache) put(scope, id string, item object, clientCall ...object) {
	if c == nil || id == "" {
		return
	}
	raw, err := json.Marshal(item)
	if err != nil || len(raw) > 1<<20 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]*list.Element)
	}
	key := scope + "\x00" + id
	if old := c.entries[key]; old != nil {
		// Only replayEntry values are inserted into this private list.
		entry, _ := old.Value.(replayEntry)
		c.bytes -= len(entry.raw)
		c.order.Remove(old)
	}
	var signature string
	if len(clientCall) == 1 {
		signature = historyCallFingerprint(clientCall[0])
	}
	c.entries[key] = c.order.PushBack(replayEntry{key: key, raw: raw, callFingerprint: signature})
	c.bytes += len(raw)
	for len(c.entries) > 1024 || c.bytes > 16<<20 {
		old := c.order.Front()
		entry, _ := old.Value.(replayEntry)
		delete(c.entries, entry.key)
		c.bytes -= len(entry.raw)
		c.order.Remove(old)
	}
}

func (c *ReplayCache) get(scope, id string) object {
	return c.getMatching(scope, id, "", false)
}

// A complete client call is stronger evidence than a reused call ID. Matching
// ignores wire-only item IDs/status and JSON object order, but retains the tool
// kind, namespace, argument values and exact custom input.
func historyCallFingerprint(item object) string {
	kind, id, name := text(item["type"]), text(item["call_id"]), text(item["name"])
	if id == "" || name == "" || strings.TrimSpace(id) != id || strings.TrimSpace(name) != name {
		return ""
	}
	namespace := ""
	if raw, exists := item["namespace"]; exists {
		var ok bool
		namespace, ok = raw.(string)
		if !ok || strings.TrimSpace(namespace) != namespace {
			return ""
		}
	}
	canonical := object{"type": kind, "call_id": id, "name": name, "namespace": namespace}
	switch kind {
	case "function_call":
		arguments := item["arguments"]
		if raw, ok := arguments.(string); ok {
			if decode([]byte(raw), &arguments) != nil {
				return ""
			}
		}
		if args, ok := arguments.(object); !ok || args == nil {
			return ""
		}
		canonical["arguments"] = arguments
	case "custom_tool_call":
		input, ok := item["input"].(string)
		if !ok {
			return ""
		}
		canonical["input"] = input
	default:
		return ""
	}
	return fingerprint(canonical)
}

func (c *ReplayCache) getForCall(scope, id string, clientCall object) object {
	signature := historyCallFingerprint(clientCall)
	if signature == "" {
		return nil
	}
	return c.getMatching(scope, id, signature, true)
}

func (c *ReplayCache) getMatching(scope, id, signature string, requireSignature bool) object {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.entries[scope+"\x00"+id]
	if entry == nil {
		return nil
	}
	cached, ok := entry.Value.(replayEntry)
	if !ok || (requireSignature && cached.callFingerprint != signature) {
		return nil
	}
	c.order.MoveToBack(entry)
	var item object
	if decode(cached.raw, &item) != nil {
		return nil
	}
	return item
}

func (b *Bridge) collectTools(value any, namespace string) ([]any, error) {
	var catalog []any
	items, _ := value.([]any)
	for _, raw := range items {
		item, ok := raw.(object)
		if !ok {
			return nil, fmt.Errorf("invalid Basispoints client tool")
		}
		kind, name := text(item["type"]), text(item["name"])
		if kind == "namespace" {
			if name == "" {
				return nil, fmt.Errorf("basispoints client namespaces require a name")
			}
			nestedNamespace := name
			if namespace != "" {
				nestedNamespace = namespace + "." + name
			}
			nested, err := b.collectTools(item["tools"], nestedNamespace)
			if err != nil {
				return nil, err
			}
			catalog = append(catalog, nested...)
			continue
		}
		if isUnsupportedHostedTool(kind) {
			if b.unsupportedTools == nil {
				b.unsupportedTools = make(map[string]bool)
			}
			b.unsupportedTools[kind] = true
			continue
		}
		if kind != "function" && kind != "custom" {
			return nil, fmt.Errorf("basispoints does not support hosted tool %q; use client function or custom tools", kind)
		}
		if name == "" {
			return nil, fmt.Errorf("basispoints client tools require a name")
		}
		key := name
		if namespace != "" {
			key = namespace + "." + name
		}
		entry := object{"type": kind, "name": key}
		for _, field := range []string{"description", "format", "parameters"} {
			if v, exists := item[field]; exists {
				entry[field] = v
			}
		}
		if kind == "function" && entry["parameters"] == nil {
			entry["parameters"] = item["inputSchema"]
			if entry["parameters"] == nil {
				entry["parameters"] = item["input_schema"]
			}
		}
		definition := fingerprint(item)
		if previous, exists := b.tools[key]; exists {
			if previous.Definition != definition || previous.Namespace != namespace || previous.Name != name {
				return nil, fmt.Errorf("conflicting duplicate Basispoints client tool %q", key)
			}
			continue
		}
		parameters, _ := entry["parameters"].(object)
		b.tools[key] = tool{Name: name, Namespace: namespace, Kind: kind, Definition: definition, Parameters: parameters}
		catalog = append(catalog, entry)
	}
	return catalog, nil
}

// Hosted capabilities cannot be relayed as client function calls. Ignore known
// declarations in automatic mode; forced selections are rejected by Prepare.
func isUnsupportedHostedTool(kind string) bool {
	switch kind {
	case "web_search", "web_search_preview", "web_search_preview_2025_03_11", "web_search_2025_08_26",
		"tool_search", "image_generation", "file_search", "code_interpreter", "computer", "computer_use_preview", "mcp":
		return true
	default:
		return false
	}
}

// rebuildNativeHistoryCall uses only the complete call supplied by the client.
// It does not execute a tool or require that an old tool remain in today's
// catalog. Cached native items remain authoritative when available.
func rebuildNativeHistoryCall(item object) (object, error) {
	id, name := text(item["call_id"]), text(item["name"])
	if id == "" || strings.TrimSpace(id) != id || name == "" || strings.TrimSpace(name) != name {
		return nil, fmt.Errorf("basispoints history recovery requires a complete tool call with nonempty call_id and name")
	}
	if value, exists := item["namespace"]; exists {
		namespace, ok := value.(string)
		if !ok || strings.TrimSpace(namespace) != namespace {
			return nil, fmt.Errorf("basispoints history tool namespace must be a string")
		}
		if namespace != "" {
			name = namespace + "." + name
		}
	}
	envelope := object{"name": name}
	switch text(item["type"]) {
	case "function_call":
		arguments := item["arguments"]
		if encoded, ok := arguments.(string); ok {
			if decode([]byte(encoded), &arguments) != nil {
				return nil, fmt.Errorf("basispoints history function arguments must contain one valid JSON object")
			}
		}
		if args, ok := arguments.(object); !ok || args == nil {
			return nil, fmt.Errorf("basispoints history function arguments must be a JSON object")
		}
		envelope["arguments"] = arguments
	case "custom_tool_call":
		input, ok := item["input"].(string)
		if !ok {
			return nil, fmt.Errorf("basispoints history custom tool input must be a string")
		}
		envelope["input"] = input
	default:
		return nil, fmt.Errorf("basispoints history recovery requires a function or custom tool call")
	}
	code, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("basispoints history tool arguments cannot be serialized")
	}
	arguments, err := json.Marshal(object{
		"code": string(code), "summary": "Replay a previously requested client tool",
		"extended_summary": "The supplied client history contains this tool call; consume its recorded result without repeating it.",
		"destructive":      false, "references": []any{},
	})
	if err != nil {
		return nil, fmt.Errorf("basispoints history transport cannot be serialized")
	}
	itemID := text(item["id"])
	if !strings.HasPrefix(itemID, "fc_") || len(itemID) > 64 {
		itemID = "fc_" + fingerprint(id)
	}
	return object{
		"type": "function_call", "id": itemID, "call_id": id, "name": "run_officejs",
		"arguments": string(arguments), "status": "completed",
	}, nil
}

func (b *Bridge) translateHistory(input []any) ([]any, error) {
	result := make([]any, 0, len(input))
	seenCalls := make(map[string]bool)
	var trigger any
	for _, raw := range input {
		item, ok := raw.(object)
		if !ok {
			return nil, fmt.Errorf("invalid Basispoints input item")
		}
		delete(item, "internal_chat_message_metadata_passthrough")
		switch text(item["type"]) {
		case "additional_tools":
			continue
		case "item_reference":
			return nil, fmt.Errorf("basispoints requires full history; item_reference is unsupported")
		case "compaction_trigger":
			trigger = item
			continue
		case "reasoning":
			if encrypted := text(item["encrypted_content"]); encrypted != "" {
				result = append(result, object{"type": "reasoning", "summary": []any{}, "encrypted_content": encrypted})
			}
			continue
		case "function_call", "custom_tool_call":
			id := text(item["call_id"])
			if native := b.replay.getForCall(b.scope, id, item); native != nil {
				item = native
			} else {
				native, err := rebuildNativeHistoryCall(item)
				if err != nil {
					return nil, err
				}
				b.replay.put(b.scope, id, native, item)
				item = native
			}
			seenCalls[id] = true
		case "function_call_output", "custom_tool_call_output":
			id := text(item["call_id"])
			if !seenCalls[id] {
				native := b.replay.get(b.scope, id)
				if native == nil {
					return nil, fmt.Errorf("basispoints original tool item is unavailable for this tool result; start a new conversation")
				}
				result = append(result, native)
				seenCalls[id] = true
			}
			item["type"] = "function_call_output"
			if err := validateHistoryContent(item["output"]); err != nil {
				return nil, err
			}
			// Codex custom results carry ctco_ IDs. After lowering to a function
			// result, BPS requires an fc_ item ID even when the client supplied one.
			itemID := text(item["id"])
			if itemID == "" {
				itemID = "fc_" + id
			}
			if !strings.HasPrefix(itemID, "fc_") || len(itemID) > 64 {
				itemID = "fc_" + fingerprint(itemID)
			}
			item["id"] = itemID
		case "configuration_update":
			return nil, fmt.Errorf("basispoints does not support configuration_update; start a new request with the desired effort")
		}
		if err := validateHistoryContent(item["content"]); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	if trigger != nil {
		result = append(result, trigger)
	}
	return result, nil
}

func validateHistoryContent(value any) error {
	content, _ := value.([]any)
	for _, rawPart := range content {
		part, _ := rawPart.(object)
		switch text(part["type"]) {
		case "input_text", "output_text", "text", "refusal":
		case "input_image":
			if err := validateImage(part); err != nil {
				return err
			}
		default:
			return fmt.Errorf("basispoints supports text and HTTPS input_image content only")
		}
	}
	return nil
}

func isTool(item object) bool {
	return text(item["type"]) == "function_call" || text(item["type"]) == "custom_tool_call"
}

// translateCall accepts only the declared relay transport and a caller-declared tool.
// It does not evaluate code or dispatch any Excel operation.
func (b *Bridge) translateCall(native object) (object, error) {
	name := text(native["name"])
	if text(native["type"]) == "function_call" && (name == "update_plan" || name == "functions.update_plan") {
		return b.translateNativePlan(native)
	}
	if name != "run_officejs" && name != "functions.run_officejs" {
		return b.translateDirectCatalogCall(native)
	}
	var arguments object
	if value, ok := native["arguments"].(object); ok {
		arguments = value
	} else if err := decode([]byte(text(native["arguments"])), &arguments); err != nil {
		return nil, fmt.Errorf("basispoints returned invalid tool transport arguments")
	}
	if arguments == nil {
		return nil, fmt.Errorf("basispoints returned empty tool transport arguments")
	}
	envelope, marked, err := customTransportEnvelope(arguments)
	if !marked && err == nil {
		envelope, err = decodeTransportEnvelope(arguments["code"])
	}
	if err != nil {
		return nil, err
	}
	toolName, err := envelopeName(envelope)
	if err != nil {
		return nil, err
	}
	info, allowed := b.tools[toolName]
	if !allowed {
		return nil, fmt.Errorf("basispoints returned a tool outside the client's catalog")
	}
	result, err := b.finishClientToolCall(native, info, envelope, marked)
	if err != nil {
		return nil, err
	}
	// run_officejs is a real BPS-native tool, so its item replays upstream verbatim.
	b.replay.put(b.scope, text(native["call_id"]), native, result)
	return result, nil
}

// translateDirectCatalogCall recovers a native tool call the model addressed by the
// client tool's own name instead of through the run_officejs transport. Some turns
// skip the wrapper and call the catalog tool directly; the reference plugins accept
// this rather than failing the whole response. It relays the client's declared tool
// call to the client unchanged and never executes any code. Only exact catalog names
// (optionally carrying a host "functions." display prefix) are accepted; any other
// native tool remains an unsupported-native-tool error.
func (b *Bridge) translateDirectCatalogCall(native object) (object, error) {
	name := text(native["name"])
	info, ok := b.tools[name]
	if !ok {
		if trimmed := strings.TrimPrefix(name, "functions."); trimmed != name {
			info, ok = b.tools[trimmed]
		}
	}
	if !ok {
		return nil, fmt.Errorf("basispoints returned an unsupported native tool; no tool was executed")
	}
	kind := text(native["type"])
	var envelope object
	switch info.Kind {
	case "function":
		if kind != "function_call" {
			return nil, fmt.Errorf("basispoints returned client function tool %q as a %q; no tool was executed", info.Name, kind)
		}
		envelope = object{"name": info.Name, "arguments": native["arguments"]}
	case "custom":
		if kind != "custom_tool_call" {
			return nil, fmt.Errorf("basispoints returned client custom tool %q as a %q; no tool was executed", info.Name, kind)
		}
		input, ok := native["input"].(string)
		if !ok {
			return nil, fmt.Errorf("basispoints direct custom tool input must be a string")
		}
		envelope = object{"name": info.Name, "input": input}
	default:
		return nil, fmt.Errorf("basispoints returned an unsupported native tool; no tool was executed")
	}
	result, err := b.finishClientToolCall(native, info, envelope, false)
	if err != nil {
		return nil, err
	}
	// The model bypassed run_officejs, so the bare native name is not a BPS tool.
	// Cache a transport-wrapped replay so the next turn presents a BPS-known
	// run_officejs item, matching how absent history is rebuilt.
	wrapped, err := rebuildNativeHistoryCall(result)
	if err != nil {
		return nil, err
	}
	b.replay.put(b.scope, text(native["call_id"]), wrapped, result)
	return result, nil
}

// finishClientToolCall builds the client-facing tool item from a resolved catalog
// tool and its envelope. It performs no caching and executes nothing; callers decide
// how the call replays upstream.
func (b *Bridge) finishClientToolCall(native object, info tool, envelope object, marked bool) (object, error) {
	if marked && info.Kind != "custom" {
		return nil, fmt.Errorf("basispoints raw transport requires a declared custom tool")
	}
	id := text(native["call_id"])
	if id == "" {
		return nil, fmt.Errorf("basispoints tool call is missing call_id")
	}
	itemID := text(native["id"])
	if itemID == "" {
		itemID = "fc_" + fingerprint(id)
	}
	result := object{"type": info.Kind + "_call", "id": itemID, "call_id": id, "name": info.Name, "status": "completed"}
	if info.Namespace != "" {
		result["namespace"] = info.Namespace
	}
	if info.Kind == "custom" {
		value, hasInput := envelope["input"]
		if alias, hasAlias := envelope["args"]; hasAlias {
			if hasInput {
				return nil, fmt.Errorf("basispoints custom tool envelope contains conflicting input fields")
			}
			value = alias
		}
		if _, exists := envelope["arguments"]; exists {
			return nil, fmt.Errorf("basispoints custom tools require input text, not arguments")
		}
		input, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("basispoints custom tool input must be a string")
		}
		result["type"] = "custom_tool_call"
		result["id"] = "ctc_" + fingerprint(id)
		result["input"] = input
	} else {
		args, err := envelopeArguments(envelope)
		if err != nil {
			return nil, err
		}
		if raw, ok := args.(string); ok {
			if decode([]byte(raw), &args) != nil {
				return nil, fmt.Errorf("basispoints function arguments are invalid JSON")
			}
		}
		if _, ok := args.(object); !ok {
			return nil, fmt.Errorf("basispoints function arguments must be an object")
		}
		encoded, _ := json.Marshal(args)
		result["arguments"] = string(encoded)
	}
	return result, nil
}

func (b *Bridge) translateResponse(response object) error {
	if response == nil {
		return nil
	}
	output, _ := response["output"].([]any)
	for i, raw := range output {
		item, _ := raw.(object)
		if isTool(item) {
			translated, err := b.translateCall(item)
			if err != nil {
				return err
			}
			output[i] = translated
		}
	}
	response["reasoning"] = object{"effort": b.Effort}
	response["parallel_tool_calls"] = false
	return nil
}

func isToolEvent(kind string) bool {
	return strings.HasPrefix(kind, "response.function_call_arguments.") || strings.HasPrefix(kind, "response.custom_tool_call_input.")
}
