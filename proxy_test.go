package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGrokRemovesClientMetadata(t *testing.T) {
	var upstream map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Fatalf("authorization = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&upstream); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\"}}\n\n")
	}))
	defer server.Close()

	proxy, err := newProxy("secret", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"grok-4.5","client_metadata":{"app":"codex"},"stream":true,"tools":[{"type":"namespace","name":"team","tools":[{"type":"function","name":"delegate","parameters":{"type":"object"}}]}]}`))
	recorder := httptest.NewRecorder()
	proxy.handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if _, exists := upstream["client_metadata"]; exists {
		t.Fatal("client_metadata reached OpenCode")
	}
	tools := upstream["tools"].([]any)
	if tools[0].(map[string]any)["type"] != "function" {
		t.Fatalf("tools were not flattened: %#v", tools)
	}
}

func TestResponsesToChatConvertsCustomToolsAndHistory(t *testing.T) {
	request := map[string]any{
		"model": "deepseek-v4-flash", "instructions": "be concise", "stream": true,
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "list files"}}},
			map[string]any{"type": "custom_tool_call_output", "call_id": "call_1", "output": "a.txt"},
		},
		"tools": []any{map[string]any{"type": "custom", "name": "shell", "description": "run a command"}},
	}

	chat, custom, err := responsesToChat(request)
	if err != nil {
		t.Fatal(err)
	}
	if !custom["shell"] {
		t.Fatal("custom shell tool was not remembered")
	}
	messages := chat["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("messages = %#v", messages)
	}
	tools := chat["tools"].([]any)
	function := tools[0].(map[string]any)["function"].(map[string]any)
	if function["name"] != "shell" {
		t.Fatalf("tool = %#v", function)
	}
}

func TestDeepSeekStreamBecomesResponsesEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"chat_1\",\"choices\":[{\"delta\":{\"content\":\"OK\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"chat_1\",\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1,\"total_tokens\":4}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	proxy, err := newProxy("secret", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	payload := `{"model":"deepseek-v4-flash","stream":true,"input":"Say OK"}`
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(payload))
	recorder := httptest.NewRecorder()
	proxy.handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, expected := range []string{"response.created", "response.output_item.added", "response.output_text.delta", "\"delta\":\"OK\"", "response.completed", "\"input_tokens\":3"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("response missing %q: %s", expected, body)
		}
	}
}
