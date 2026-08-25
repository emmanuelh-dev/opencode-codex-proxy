package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	grokModel     = "grok-4.5"
	deepSeekModel = "deepseek-v4-flash"
)

type proxy struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

func newProxy(apiKey, baseURL string) (*proxy, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("OPENCODE_GO_API_KEY is required")
	}
	return &proxy{
		apiKey:  apiKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Timeout: 15 * time.Minute},
	}, nil
}

func (p *proxy) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /v1/models", p.models)
	mux.HandleFunc("POST /v1/responses", p.responses)
	return mux
}

func (p *proxy) models(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"models": []any{}})
}

func (p *proxy) responses(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var request map[string]any
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, "invalid JSON request", http.StatusBadRequest)
		return
	}

	model, _ := request["model"].(string)
	switch model {
	case grokModel:
		delete(request, "client_metadata")
		request["tools"] = flattenNamespaces(request["tools"])
		p.forwardResponses(w, r, request)
	case deepSeekModel:
		p.forwardDeepSeek(w, r, request)
	default:
		http.Error(w, "unsupported model: "+model, http.StatusBadRequest)
	}
}

func flattenNamespaces(value any) []any {
	tools, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]any, 0, len(tools))
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok || tool["type"] != "namespace" {
			result = append(result, raw)
			continue
		}
		result = append(result, flattenNamespaces(tool["tools"])...)
	}
	return result
}

func (p *proxy) forwardResponses(w http.ResponseWriter, r *http.Request, request map[string]any) {
	response, err := p.post(r, "/responses", request)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	copyResponse(w, response)
}

func (p *proxy) forwardDeepSeek(w http.ResponseWriter, r *http.Request, request map[string]any) {
	chatRequest, customTools, err := responsesToChat(request)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if stream, _ := chatRequest["stream"].(bool); !stream {
		http.Error(w, "only streaming Responses requests are supported", http.StatusBadRequest)
		return
	}

	response, err := p.post(r, "/chat/completions", chatRequest)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		copyResponse(w, response)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flush, _ := w.(http.Flusher)
	if err := translateChatStream(w, response.Body, customTools, flush); err != nil {
		return
	}
}

func (p *proxy) post(r *http.Request, path string, payload map[string]any) (*http.Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode upstream request: %w", err)
	}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, p.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create upstream request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+p.apiKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream, application/json")
	return p.client.Do(request)
}

func responsesToChat(request map[string]any) (map[string]any, map[string]bool, error) {
	messages := make([]any, 0)
	if instructions, _ := request["instructions"].(string); instructions != "" {
		messages = append(messages, map[string]any{"role": "system", "content": instructions})
	}
	if err := appendInputMessages(&messages, request["input"]); err != nil {
		return nil, nil, err
	}

	tools, customTools, err := chatTools(request["tools"])
	if err != nil {
		return nil, nil, err
	}
	chat := map[string]any{
		"model":               request["model"],
		"messages":            messages,
		"stream":              true,
		"stream_options":      map[string]any{"include_usage": true},
		"parallel_tool_calls": request["parallel_tool_calls"],
	}
	if len(tools) > 0 {
		chat["tools"] = tools
		chat["tool_choice"] = chatToolChoice(request["tool_choice"])
	}
	copyNumber(request, chat, "temperature")
	copyNumber(request, chat, "top_p")
	copyNumber(request, chat, "max_output_tokens", "max_tokens")
	return chat, customTools, nil
}

func appendInputMessages(messages *[]any, input any) error {
	if text, ok := input.(string); ok {
		*messages = append(*messages, map[string]any{"role": "user", "content": text})
		return nil
	}
	items, ok := input.([]any)
	if !ok {
		return errors.New("Responses input must be text or an array of items")
	}
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			return errors.New("Responses input contains an invalid item")
		}
		switch item["type"] {
		case "message":
			content, err := itemText(item["content"])
			if err != nil {
				return err
			}
			role, _ := item["role"].(string)
			if role == "" {
				role = "user"
			}
			*messages = append(*messages, map[string]any{"role": role, "content": content})
		case "function_call", "custom_tool_call":
			name, _ := item["name"].(string)
			callID, _ := item["call_id"].(string)
			arguments, _ := item["arguments"].(string)
			if item["type"] == "custom_tool_call" {
				input, _ := item["input"].(string)
				encoded, _ := json.Marshal(map[string]string{"input": input})
				arguments = string(encoded)
			}
			*messages = append(*messages, map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
				"id": callID, "type": "function", "function": map[string]any{"name": name, "arguments": arguments},
			}}})
		case "function_call_output", "custom_tool_call_output":
			content, err := itemText(item["output"])
			if err != nil {
				return err
			}
			*messages = append(*messages, map[string]any{"role": "tool", "tool_call_id": item["call_id"], "content": content})
		case "reasoning":
		default:
			return fmt.Errorf("unsupported Responses input item type %q", item["type"])
		}
	}
	return nil
}

func itemText(value any) (string, error) {
	if text, ok := value.(string); ok {
		return text, nil
	}
	parts, ok := value.([]any)
	if !ok {
		return "", nil
	}
	text := make([]string, 0, len(parts))
	for _, raw := range parts {
		part, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch part["type"] {
		case "input_text", "output_text", "text":
			if value, ok := part["text"].(string); ok {
				text = append(text, value)
			}
		case "refusal":
			if value, ok := part["refusal"].(string); ok {
				text = append(text, value)
			}
		default:
			return "", fmt.Errorf("unsupported content type %q", part["type"])
		}
	}
	return strings.Join(text, "\n"), nil
}

func chatTools(value any) ([]any, map[string]bool, error) {
	tools := flattenNamespaces(value)
	if len(tools) == 0 {
		return nil, map[string]bool{}, nil
	}
	result := make([]any, 0, len(tools))
	custom := make(map[string]bool)
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			return nil, nil, errors.New("invalid Responses tool")
		}
		switch tool["type"] {
		case "function":
			function := cloneMap(tool)
			delete(function, "type")
			result = append(result, map[string]any{"type": "function", "function": function})
		case "custom":
			name, _ := tool["name"].(string)
			if name == "" {
				return nil, nil, errors.New("custom tool has no name")
			}
			custom[name] = true
			result = append(result, map[string]any{"type": "function", "function": map[string]any{
				"name": name, "description": tool["description"],
				"parameters": map[string]any{"type": "object", "properties": map[string]any{
					"input": map[string]any{"type": "string"},
				}, "required": []string{"input"}, "additionalProperties": false},
			}})
		default:
			continue
		}
	}
	return result, custom, nil
}

func chatToolChoice(value any) any {
	choice, ok := value.(map[string]any)
	if !ok {
		return value
	}
	if choice["type"] == "function" {
		if name, ok := choice["name"].(string); ok {
			return map[string]any{"type": "function", "function": map[string]any{"name": name}}
		}
	}
	return choice
}

func copyNumber(source, target map[string]any, sourceKey string, targetKey ...string) {
	value, ok := source[sourceKey]
	if !ok {
		return
	}
	key := sourceKey
	if len(targetKey) > 0 {
		key = targetKey[0]
	}
	target[key] = value
}

func translateChatStream(w io.Writer, body io.Reader, customTools map[string]bool, flush http.Flusher) error {
	responseID := "resp_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	writeSSE(w, "response.created", map[string]any{"type": "response.created", "response": map[string]any{"id": responseID}})
	flush.Flush()

	toolCalls := map[int]*chatToolCall{}
	textItemStarted := false
	textItemID := "msg_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	var outputText strings.Builder
	var usage map[string]any
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk map[string]any
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return fmt.Errorf("decode chat stream: %w", err)
		}
		if id, ok := chunk["id"].(string); ok && id != "" {
			responseID = "resp_" + id
		}
		if value, ok := chunk["usage"].(map[string]any); ok {
			usage = value
		}
		if err := consumeChatChoices(w, chunk, toolCalls, &textItemStarted, textItemID, &outputText, flush); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read chat stream: %w", err)
	}

	indexes := make([]int, 0, len(toolCalls))
	for index := range toolCalls {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	for _, index := range indexes {
		call := toolCalls[index]
		if call.ID == "" || call.Name == "" {
			continue
		}
		item := map[string]any{"id": "fc_" + call.ID, "call_id": call.ID, "name": call.Name}
		if customTools[call.Name] {
			item["type"] = "custom_tool_call"
			item["id"] = "ctc_" + call.ID
			item["input"] = customInput(call.Arguments)
		} else {
			item["type"] = "function_call"
			item["arguments"] = call.Arguments
		}
		writeSSE(w, "response.output_item.done", map[string]any{"type": "response.output_item.done", "item": item})
		flush.Flush()
	}
	if textItemStarted {
		writeSSE(w, "response.output_item.done", map[string]any{"type": "response.output_item.done", "item": map[string]any{
			"id": textItemID, "type": "message", "role": "assistant", "phase": "commentary",
			"content": []any{map[string]any{"type": "output_text", "text": outputText.String()}},
		}})
		flush.Flush()
	}

	completed := map[string]any{"type": "response.completed", "response": map[string]any{"id": responseID}}
	if usage != nil {
		completed["response"].(map[string]any)["usage"] = responseUsage(usage)
	}
	writeSSE(w, "response.completed", completed)
	flush.Flush()
	return nil
}

type chatToolCall struct {
	ID        string
	Name      string
	Arguments string
}

func consumeChatChoices(w io.Writer, chunk map[string]any, toolCalls map[int]*chatToolCall, textItemStarted *bool, textItemID string, outputText *strings.Builder, flush http.Flusher) error {
	choices, _ := chunk["choices"].([]any)
	for _, raw := range choices {
		choice, _ := raw.(map[string]any)
		delta, _ := choice["delta"].(map[string]any)
		if content, ok := delta["content"].(string); ok && content != "" {
			if !*textItemStarted {
				writeSSE(w, "response.output_item.added", map[string]any{"type": "response.output_item.added", "item": map[string]any{
					"id": textItemID, "type": "message", "role": "assistant", "content": []any{},
				}})
				flush.Flush()
				*textItemStarted = true
			}
			outputText.WriteString(content)
			writeSSE(w, "response.output_text.delta", map[string]any{"type": "response.output_text.delta", "delta": content})
			flush.Flush()
		}
		for _, rawCall := range asSlice(delta["tool_calls"]) {
			call, _ := rawCall.(map[string]any)
			index := int(number(call["index"]))
			aggregate := toolCalls[index]
			if aggregate == nil {
				aggregate = &chatToolCall{}
				toolCalls[index] = aggregate
			}
			if id, ok := call["id"].(string); ok {
				aggregate.ID = id
			}
			function, _ := call["function"].(map[string]any)
			if name, ok := function["name"].(string); ok {
				aggregate.Name = name
			}
			if arguments, ok := function["arguments"].(string); ok {
				aggregate.Arguments += arguments
			}
		}
	}
	return nil
}

func customInput(arguments string) string {
	var input struct {
		Input string `json:"input"`
	}
	if json.Unmarshal([]byte(arguments), &input) == nil && input.Input != "" {
		return input.Input
	}
	return arguments
}

func responseUsage(usage map[string]any) map[string]any {
	prompt := number(usage["prompt_tokens"])
	completion := number(usage["completion_tokens"])
	total := number(usage["total_tokens"])
	return map[string]any{
		"input_tokens":         prompt,
		"input_tokens_details": map[string]any{"cached_tokens": 0},
		"output_tokens":        completion,
		"total_tokens":         total,
	}
}

func number(value any) int64 {
	switch value := value.(type) {
	case float64:
		return int64(value)
	case int:
		return int64(value)
	case int64:
		return value
	case json.Number:
		result, _ := value.Int64()
		return result
	default:
		return 0
	}
}

func asSlice(value any) []any {
	items, _ := value.([]any)
	return items
}

func cloneMap(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func copyResponse(w http.ResponseWriter, response *http.Response) {
	for key, values := range response.Header {
		if key == "Content-Length" || key == "Connection" || key == "Transfer-Encoding" {
			continue
		}
		w.Header()[key] = values
	}
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeSSE(w io.Writer, event string, value any) {
	data, _ := json.Marshal(value)
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
}

func loadDotEnv(path string) error {
	contents, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(contents), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" {
			return fmt.Errorf("invalid .env entry %q", line)
		}
		value = strings.Trim(value, "\"'")
		if os.Getenv(key) == "" {
			_ = os.Setenv(key, value)
		}
	}
	return nil
}
