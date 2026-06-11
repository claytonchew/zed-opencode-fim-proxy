package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func newTestProxy() *Proxy {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	return NewProxy(
		"https://opencode.ai/zen/go/v1/chat/completions",
		"deepseek-v4-flash",
		10,
		logger,
	)
}

func TestTranslateRequest(t *testing.T) {
	proxy := newTestProxy()

	// Test basic request translation with FIM tokens
	maxTokens := uint32(100)
	req := &CompletionsRequest{
		Model:     "deepseek-v4-flash",
		Prompt:    "<｜fim▁begin｜>prefix<｜fim▁hole｜>suffix<｜fim▁end｜>",
		MaxTokens: &maxTokens,
		Stop:      []string{"\n"},
	}

	chatReq := proxy.translateRequest(req)

	// Verify model is preserved
	if chatReq.Model != "deepseek-v4-flash" {
		t.Errorf("expected model 'deepseek-v4-flash', got '%s'", chatReq.Model)
	}

	// Verify messages are created correctly
	if len(chatReq.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(chatReq.Messages))
	}

	// Verify system message for FIM parsing
	if chatReq.Messages[0].Role != "system" {
		t.Errorf("expected first message role 'system', got '%s'", chatReq.Messages[0].Role)
	}
	if chatReq.Messages[0].Content != "You are a code completion engine. Generate ONLY the exact code that should be inserted at the cursor position. CRITICAL: Do NOT repeat any code that already exists before or after the cursor. Generate only what is missing. Be minimal and concise. No explanations, no markdown, no code blocks, no comments." {
		t.Errorf("unexpected system message content: %s", chatReq.Messages[0].Content)
	}

	// Verify user message has explicit prefix/suffix format
	if chatReq.Messages[1].Role != "user" {
		t.Errorf("expected second message role 'user', got '%s'", chatReq.Messages[1].Role)
	}
	expectedContent := "Code before cursor:\nprefix\n\nCode after cursor:\nsuffix\n\nInsert ONLY what is missing between them. Do NOT repeat existing code:"
	if chatReq.Messages[1].Content != expectedContent {
		t.Errorf("unexpected user message content:\ngot:      %q\nexpected: %q", chatReq.Messages[1].Content, expectedContent)
	}

	// Verify max_tokens
	if chatReq.MaxTokens == nil || *chatReq.MaxTokens != 100 {
		t.Errorf("expected max_tokens 100, got %v", chatReq.MaxTokens)
	}

	// Verify stop
	if len(chatReq.Stop) != 1 || chatReq.Stop[0] != "\n" {
		t.Errorf("expected stop ['\\n'], got %v", chatReq.Stop)
	}

	// Verify thinking is disabled
	if chatReq.Thinking == nil {
		t.Fatal("expected thinking config, got nil")
	}
	if chatReq.Thinking.Type != "disabled" {
		t.Errorf("expected thinking type 'disabled', got '%s'", chatReq.Thinking.Type)
	}
}

func TestTranslateRequestThinkingDisabled(t *testing.T) {
	proxy := newTestProxy()

	req := &CompletionsRequest{
		Model:  "deepseek-v4-flash",
		Prompt: "test prompt",
	}

	chatReq := proxy.translateRequest(req)

	// Verify thinking config is present and disabled
	if chatReq.Thinking == nil {
		t.Fatal("expected thinking config, got nil")
	}
	if chatReq.Thinking.Type != "disabled" {
		t.Errorf("expected thinking type 'disabled', got '%s'", chatReq.Thinking.Type)
	}

	// Verify JSON serialization includes thinking
	jsonBytes, err := json.Marshal(chatReq)
	if err != nil {
		t.Fatalf("failed to marshal chat request: %v", err)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(jsonBytes, &raw); err != nil {
		t.Fatalf("failed to unmarshal to raw map: %v", err)
	}

	thinking, ok := raw["thinking"]
	if !ok {
		t.Fatal("expected 'thinking' field in JSON output")
	}
	thinkingMap, ok := thinking.(map[string]interface{})
	if !ok {
		t.Fatalf("expected thinking to be a map, got %T", thinking)
	}
	if thinkingMap["type"] != "disabled" {
		t.Errorf("expected JSON thinking.type 'disabled', got '%s'", thinkingMap["type"])
	}
}

func TestTranslateRequestDefaultModel(t *testing.T) {
	proxy := newTestProxy()

	// Test request without model (should use proxy default)
	req := &CompletionsRequest{
		Prompt: "test prompt",
	}

	chatReq := proxy.translateRequest(req)

	if chatReq.Model != "deepseek-v4-flash" {
		t.Errorf("expected default model 'deepseek-v4-flash', got '%s'", chatReq.Model)
	}
}

func TestTranslateResponse(t *testing.T) {
	proxy := newTestProxy()

	// Test basic response translation
	chatResp := &ChatCompletionsResponse{
		ID:      "test-id",
		Object:  "chat.completion",
		Created: 1234567890,
		Model:   "deepseek-v4-flash",
		Choices: []ChatChoice{
			{
				Index:        0,
				Message:      Message{Role: "assistant", Content: "completed code"},
				FinishReason: "stop",
			},
		},
		Usage: ChatUsage{
			PromptTokens:     10,
			CompletionTokens: 20,
			TotalTokens:      30,
		},
	}

	compResp := proxy.translateResponse(chatResp)

	// Verify basic fields
	if compResp.ID != "test-id" {
		t.Errorf("expected ID 'test-id', got '%s'", compResp.ID)
	}
	if compResp.Object != "text_completion" {
		t.Errorf("expected object 'text_completion', got '%s'", compResp.Object)
	}
	if compResp.Created != 1234567890 {
		t.Errorf("expected created 1234567890, got %d", compResp.Created)
	}
	if compResp.Model != "deepseek-v4-flash" {
		t.Errorf("expected model 'deepseek-v4-flash', got '%s'", compResp.Model)
	}

	// Verify choices
	if len(compResp.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(compResp.Choices))
	}
	if compResp.Choices[0].Text != "completed code" {
		t.Errorf("expected text 'completed code', got '%s'", compResp.Choices[0].Text)
	}
	if compResp.Choices[0].Index != 0 {
		t.Errorf("expected index 0, got %d", compResp.Choices[0].Index)
	}
	if compResp.Choices[0].FinishReason != "stop" {
		t.Errorf("expected finish_reason 'stop', got '%s'", compResp.Choices[0].FinishReason)
	}

	// Verify usage
	if compResp.Usage.PromptTokens != 10 {
		t.Errorf("expected prompt_tokens 10, got %d", compResp.Usage.PromptTokens)
	}
	if compResp.Usage.CompletionTokens != 20 {
		t.Errorf("expected completion_tokens 20, got %d", compResp.Usage.CompletionTokens)
	}
	if compResp.Usage.TotalTokens != 30 {
		t.Errorf("expected total_tokens 30, got %d", compResp.Usage.TotalTokens)
	}
}

func TestTranslateResponseEmptyChoices(t *testing.T) {
	proxy := newTestProxy()

	// Test response with empty choices
	chatResp := &ChatCompletionsResponse{
		ID:      "test-id",
		Object:  "chat.completion",
		Created: 1234567890,
		Model:   "deepseek-v4-flash",
		Choices: []ChatChoice{},
		Usage: ChatUsage{
			PromptTokens:     10,
			CompletionTokens: 0,
			TotalTokens:      10,
		},
	}

	compResp := proxy.translateResponse(chatResp)

	if len(compResp.Choices) != 0 {
		t.Errorf("expected 0 choices, got %d", len(compResp.Choices))
	}
	if compResp.Usage.TotalTokens != 10 {
		t.Errorf("expected total_tokens 10, got %d", compResp.Usage.TotalTokens)
	}
}

func TestFIMTokenPreservation(t *testing.T) {
	proxy := newTestProxy()

	// Test that FIM tokens are parsed and presented explicitly
	req := &CompletionsRequest{
		Prompt: "<｜fim▁begin｜>func main<｜fim▁hole｜> {",
	}

	chatReq := proxy.translateRequest(req)

	// FIM tokens should be parsed — the user message should contain explicit prefix/suffix
	expectedContent := "Code before cursor:\nfunc main\n\nCode after cursor:\n {\n\nInsert ONLY what is missing between them. Do NOT repeat existing code:"
	if chatReq.Messages[1].Content != expectedContent {
		t.Errorf("unexpected user message content:\ngot:      %q\nexpected: %q", chatReq.Messages[1].Content, expectedContent)
	}
}

func TestHandleCompletionsMethodNotAllowed(t *testing.T) {
	proxy := newTestProxy()

	req := httptest.NewRequest(http.MethodGet, "/v1/completions", nil)
	w := httptest.NewRecorder()

	proxy.handleCompletions(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status %d, got %d", http.StatusMethodNotAllowed, w.Code)
	}
}

func TestHandleCompletionsFullFlow(t *testing.T) {
	// Mock upstream server
	mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify Accept header
		if r.Header.Get("Accept") != "application/json" {
			t.Errorf("expected Accept header 'application/json', got '%s'", r.Header.Get("Accept"))
		}

		// Verify Content-Type
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected Content-Type 'application/json', got '%s'", r.Header.Get("Content-Type"))
		}

		// Verify Authorization
		if r.Header.Get("Authorization") != "Bearer test-api-key" {
			t.Errorf("expected Authorization 'Bearer test-api-key', got '%s'", r.Header.Get("Authorization"))
		}

		// Parse incoming request
		var chatReq ChatCompletionsRequest
		if err := json.NewDecoder(r.Body).Decode(&chatReq); err != nil {
			t.Errorf("failed to decode upstream request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		// Verify thinking is disabled in upstream request
		if chatReq.Thinking == nil {
			t.Error("expected thinking config in upstream request, got nil")
		} else if chatReq.Thinking.Type != "disabled" {
			t.Errorf("expected thinking type 'disabled' in upstream request, got '%s'", chatReq.Thinking.Type)
		}

		// Return mock response
		resp := ChatCompletionsResponse{
			ID:      "mock-id",
			Object:  "chat.completion",
			Created: 1234567890,
			Model:   chatReq.Model,
			Choices: []ChatChoice{
				{
					Index:        0,
					Message:      Message{Role: "assistant", Content: "func main() {}"},
					FinishReason: "stop",
				},
			},
			Usage: ChatUsage{
				PromptTokens:     10,
				CompletionTokens: 5,
				TotalTokens:      15,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer mockUpstream.Close()

	// Create proxy pointing to mock upstream
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	proxy := NewProxy(mockUpstream.URL, "deepseek-v4-flash", 10, logger)

	// Create Zed-style request
	compReq := CompletionsRequest{
		Model:  "deepseek-v4-flash",
		Prompt: "<｜fim▁begin｜>func main<｜fim▁hole｜> {",
	}
	reqBody, _ := json.Marshal(compReq)

	req := httptest.NewRequest(http.MethodPost, "/v1/completions", bytes.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer test-api-key")
	w := httptest.NewRecorder()

	proxy.handleCompletions(w, req)

	// Verify response
	if w.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}

	var compResp CompletionsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &compResp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if compResp.Object != "text_completion" {
		t.Errorf("expected object 'text_completion', got '%s'", compResp.Object)
	}
	if len(compResp.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(compResp.Choices))
	}
	if compResp.Choices[0].Text != "func main() {}" {
		t.Errorf("expected text 'func main() {}', got '%s'", compResp.Choices[0].Text)
	}
}

func TestHandleCompletionsUpstreamError(t *testing.T) {
	// Mock upstream that returns an error
	mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":{"message":"internal error","type":"server_error"}}`))
	}))
	defer mockUpstream.Close()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	proxy := NewProxy(mockUpstream.URL, "deepseek-v4-flash", 10, logger)

	compReq := CompletionsRequest{
		Model:  "deepseek-v4-flash",
		Prompt: "test",
	}
	reqBody, _ := json.Marshal(compReq)

	req := httptest.NewRequest(http.MethodPost, "/v1/completions", bytes.NewReader(reqBody))
	w := httptest.NewRecorder()

	proxy.handleCompletions(w, req)

	// Verify error is passed through
	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected status %d, got %d", http.StatusInternalServerError, w.Code)
	}

	var errResp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}
}

func TestHandleHealth(t *testing.T) {
	proxy := newTestProxy()

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()

	proxy.handleHealth(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, w.Code)
	}

	if w.Header().Get("Content-Type") != "application/json" {
		t.Errorf("expected Content-Type 'application/json', got '%s'", w.Header().Get("Content-Type"))
	}

	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode health response: %v", err)
	}

	if resp["status"] != "ok" {
		t.Errorf("expected status 'ok', got '%s'", resp["status"])
	}
}

func TestHandleHealthMethodNotAllowed(t *testing.T) {
	proxy := newTestProxy()

	req := httptest.NewRequest(http.MethodPost, "/health", nil)
	w := httptest.NewRecorder()

	proxy.handleHealth(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status %d, got %d", http.StatusMethodNotAllowed, w.Code)
	}
}

func TestStripMarkdown(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "markdown with language tag",
			input:    "```typescript\nconst x = 1;\n```",
			expected: "const x = 1;",
		},
		{
			name:     "markdown without language tag",
			input:    "```\nconst x = 1;\n```",
			expected: "const x = 1;",
		},
		{
			name:     "no markdown",
			input:    "const x = 1;",
			expected: "const x = 1;",
		},
		{
			name:     "just opening fence",
			input:    "```typescript\n",
			expected: "",
		},
		{
			name:     "multi-line python",
			input:    "```python\ndef foo():\n    return 1\n```",
			expected: "def foo():\n    return 1",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "whitespace only",
			input:    "   \n  \n  ",
			expected: "   \n  \n  ",
		},
		{
			name:     "fence with leading/trailing whitespace",
			input:    "  ```go\nfmt.Println(\"hello\")\n```  ",
			expected: "fmt.Println(\"hello\")",
		},
		{
			name:     "just triple backticks",
			input:    "```",
			expected: "",
		},
		{
			name:     "unclosed fence returns content after opening",
			input:    "```java\nint x = 5;",
			expected: "int x = 5;",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripMarkdown(tt.input)
			if got != tt.expected {
				t.Errorf("stripMarkdown(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestTranslateResponseMarkdownStripped(t *testing.T) {
	proxy := newTestProxy()

	// Test that markdown code blocks are stripped in translation
	chatResp := &ChatCompletionsResponse{
		ID:      "test-id",
		Object:  "chat.completion",
		Created: 1234567890,
		Model:   "deepseek-v4-flash",
		Choices: []ChatChoice{
			{
				Index:        0,
				Message:      Message{Role: "assistant", Content: "```typescript\nexport const foo = bar;\n```"},
				FinishReason: "stop",
			},
		},
		Usage: ChatUsage{
			PromptTokens:     10,
			CompletionTokens: 20,
			TotalTokens:      30,
		},
	}

	compResp := proxy.translateResponse(chatResp)

	if len(compResp.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(compResp.Choices))
	}
	if compResp.Choices[0].Text != "export const foo = bar;" {
		t.Errorf("expected text 'export const foo = bar;', got '%s'", compResp.Choices[0].Text)
	}
}

func TestParseFIMPrompt(t *testing.T) {
	tests := []struct {
		name           string
		prompt         string
		wantPrefix     string
		wantSuffix     string
		wantFound      bool
	}{
		{
			name:       "valid FIM prompt with all tokens",
			prompt:     "<｜fim▁begin｜>func main()<｜fim▁hole｜> {",
			wantPrefix: "func main()",
			wantSuffix: " {",
			wantFound:  true,
		},
		{
			name:       "missing fim_begin",
			prompt:     "func main()<｜fim▁hole｜> {<｜fim▁end｜>",
			wantPrefix: "",
			wantSuffix: "",
			wantFound:  false,
		},
		{
			name:       "missing fim_hole",
			prompt:     "<｜fim▁begin｜>func main()<｜fim▁end｜>",
			wantPrefix: "",
			wantSuffix: "",
			wantFound:  false,
		},
		{
			name:       "missing fim_end",
			prompt:     "<｜fim▁begin｜>func main()<｜fim▁hole｜> {",
			wantPrefix: "func main()",
			wantSuffix: " {",
			wantFound:  true,
		},
		{
			name:       "empty prefix and suffix",
			prompt:     "<｜fim▁begin｜><｜fim▁hole｜><｜fim▁end｜>",
			wantPrefix: "",
			wantSuffix: "",
			wantFound:  true,
		},
		{
			name:       "empty prefix only",
			prompt:     "<｜fim▁begin｜><｜fim▁hole｜>suffix<｜fim▁end｜>",
			wantPrefix: "",
			wantSuffix: "suffix",
			wantFound:  true,
		},
		{
			name:       "empty suffix only",
			prompt:     "<｜fim▁begin｜>prefix<｜fim▁hole｜><｜fim▁end｜>",
			wantPrefix: "prefix",
			wantSuffix: "",
			wantFound:  true,
		},
		{
			name: "multi-line prefix and suffix",
			prompt: "<｜fim▁begin｜>function foo() {\n  return\n<｜fim▁hole｜>\n}\n<｜fim▁end｜>",
			wantPrefix: "function foo() {\n  return\n",
			wantSuffix: "\n}\n",
			wantFound:  true,
		},
		{
			name:       "plain text without FIM tokens",
			prompt:     "just some code without tokens",
			wantPrefix: "",
			wantSuffix: "",
			wantFound:  false,
		},
		{
			name:       "partial FIM tokens only begin",
			prompt:     "<｜fim▁begin｜>something",
			wantPrefix: "",
			wantSuffix: "",
			wantFound:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prefix, suffix, found := parseFIMPrompt(tt.prompt)
			if found != tt.wantFound {
				t.Errorf("parseFIMPrompt() found = %v, want %v", found, tt.wantFound)
			}
			if prefix != tt.wantPrefix {
				t.Errorf("parseFIMPrompt() prefix = %q, want %q", prefix, tt.wantPrefix)
			}
			if suffix != tt.wantSuffix {
				t.Errorf("parseFIMPrompt() suffix = %q, want %q", suffix, tt.wantSuffix)
			}
		})
	}
}

func TestTranslateRequestNoFIMTokens(t *testing.T) {
	proxy := newTestProxy()

	// Test request without FIM tokens (fallback path)
	req := &CompletionsRequest{
		Prompt: "just some code without FIM tokens",
	}

	chatReq := proxy.translateRequest(req)

	// Verify fallback system message
	if chatReq.Messages[0].Content != "You are a code completion engine. Output ONLY the raw code. No markdown, no code blocks, no explanations." {
		t.Errorf("unexpected fallback system message content: %s", chatReq.Messages[0].Content)
	}

	// Verify user message is the raw prompt
	if chatReq.Messages[1].Content != "just some code without FIM tokens" {
		t.Errorf("unexpected user message content: %s", chatReq.Messages[1].Content)
	}
}
