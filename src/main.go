package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// CompletionsRequest is the request format Zed sends for Edit Prediction.
type CompletionsRequest struct {
	Model     string   `json:"model"`
	Prompt    string   `json:"prompt"`
	MaxTokens *uint32  `json:"max_tokens,omitempty"`
	Stop      []string `json:"stop,omitempty"`
}

// ChatCompletionsRequest is the request format OpenCode Go expects.
type ChatCompletionsRequest struct {
	Model     string          `json:"model"`
	Messages  []Message       `json:"messages"`
	MaxTokens *uint32         `json:"max_tokens,omitempty"`
	Stop      []string        `json:"stop,omitempty"`
	Thinking  *ThinkingConfig `json:"thinking,omitempty"`
}

// ThinkingConfig controls reasoning behavior for reasoning models.
type ThinkingConfig struct {
	Type string `json:"type"` // "enabled" or "disabled"
}

// Message represents a chat message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// CompletionsResponse is the response format Zed expects.
type CompletionsResponse struct {
	ID      string             `json:"id"`
	Object  string             `json:"object"`
	Created int64              `json:"created"`
	Model   string             `json:"model"`
	Choices []CompletionChoice `json:"choices"`
	Usage   CompletionUsage    `json:"usage"`
}

// CompletionChoice represents a single completion choice.
type CompletionChoice struct {
	Text         string `json:"text"`
	Index        int    `json:"index"`
	FinishReason string `json:"finish_reason"`
}

// CompletionUsage represents token usage information.
type CompletionUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ChatCompletionsResponse is the response format OpenCode Go returns.
type ChatCompletionsResponse struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"`
	Created int64        `json:"created"`
	Model   string       `json:"model"`
	Choices []ChatChoice `json:"choices"`
	Usage   ChatUsage    `json:"usage"`
}

// ChatChoice represents a single chat choice.
type ChatChoice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// ChatUsage represents token usage in chat responses.
type ChatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// stripMarkdown removes markdown code block wrappers from completion text.
// If the text is wrapped in ```lang\n...\n```, it extracts just the inner code.
func stripMarkdown(text string) string {
	trimmed := strings.TrimSpace(text)

	// Check if it starts with a markdown code block
	if !strings.HasPrefix(trimmed, "```") {
		return text
	}

	// Find the end of the opening line (```typescript, ```python, etc.)
	firstNewline := strings.Index(trimmed, "\n")
	if firstNewline == -1 {
		// Just "```" with no content — return empty
		return ""
	}

	// Extract content after the opening line
	inner := trimmed[firstNewline+1:]

	// Check if it ends with ```
	if strings.HasSuffix(inner, "```") {
		inner = inner[:len(inner)-3]
	}

	// Also handle trailing ```\n
	inner = strings.TrimSuffix(inner, "\n")

	return strings.TrimRight(inner, "\n")
}

// parseFIMPrompt extracts prefix and suffix from a FIM-formatted prompt.
// Returns (prefix, suffix, found) where found indicates if FIM tokens were present.
// Handles both formats with and without the trailing <｜fim▁end｜> token.
func parseFIMPrompt(prompt string) (prefix, suffix string, found bool) {
	// DeepSeek Coder format: <｜fim▁begin｜>{prefix}<｜fim▁hole｜>{suffix}<｜fim▁end｜>
	// Zed may omit the end token: <｜fim▁begin｜>{prefix}<｜fim▁hole｜>{suffix}
	const (
		fimBegin = "<｜fim▁begin｜>"
		fimHole  = "<｜fim▁hole｜>"
		fimEnd   = "<｜fim▁end｜>"
	)

	beginIdx := strings.Index(prompt, fimBegin)
	if beginIdx == -1 {
		return "", "", false
	}

	holeIdx := strings.Index(prompt, fimHole)
	if holeIdx == -1 {
		return "", "", false
	}

	prefix = prompt[beginIdx+len(fimBegin) : holeIdx]

	// The end token is optional — if present, use it; otherwise suffix is everything after the hole.
	suffixStart := holeIdx + len(fimHole)
	endIdx := strings.Index(prompt, fimEnd)
	if endIdx != -1 {
		suffix = prompt[suffixStart:endIdx]
	} else {
		suffix = prompt[suffixStart:]
	}

	return prefix, suffix, true
}

// Proxy holds the configuration and handles the translation logic.
type Proxy struct {
	endpoint string
	model    string
	timeout  time.Duration
	client   *http.Client
	logger   *slog.Logger
}

// NewProxy creates a new Proxy with the given configuration.
func NewProxy(endpoint, model string, timeoutSec int, logger *slog.Logger) *Proxy {
	return &Proxy{
		endpoint: endpoint,
		model:    model,
		timeout:  time.Duration(timeoutSec) * time.Second,
		client: &http.Client{
			Timeout: time.Duration(timeoutSec) * time.Second,
		},
		logger: logger,
	}
}

// translateRequest converts a Zed CompletionsRequest to an OpenCode Go ChatCompletionsRequest.
func (p *Proxy) translateRequest(req *CompletionsRequest) *ChatCompletionsRequest {
	model := p.model
	if req.Model != "" {
		model = req.Model
	}

	// Try to parse FIM tokens from the prompt
	prefix, suffix, hasFIM := parseFIMPrompt(req.Prompt)

	var messages []Message
	if hasFIM {
		// Explicit prefix/suffix format for chat models
		messages = []Message{
			{
				Role:    "system",
				Content: "You are a code completion engine. Generate ONLY the exact code that should be inserted at the cursor position. CRITICAL: Do NOT repeat any code that already exists before or after the cursor. Generate only what is missing. Be minimal and concise. No explanations, no markdown, no code blocks, no comments.",
			},
			{
				Role:    "user",
				Content: fmt.Sprintf("Code before cursor:\n%s\n\nCode after cursor:\n%s\n\nInsert ONLY what is missing between them. Do NOT repeat existing code:", prefix, suffix),
			},
		}
	} else {
		// Fallback: use the prompt as-is (shouldn't happen with Zed, but just in case)
		messages = []Message{
			{
				Role:    "system",
				Content: "You are a code completion engine. Output ONLY the raw code. No markdown, no code blocks, no explanations.",
			},
			{
				Role:    "user",
				Content: req.Prompt,
			},
		}
	}

	return &ChatCompletionsRequest{
		Model:     model,
		Messages:  messages,
		MaxTokens: req.MaxTokens,
		Stop:      req.Stop,
		Thinking:  &ThinkingConfig{Type: "disabled"},
	}
}

// translateResponse converts an OpenCode Go ChatCompletionsResponse to a Zed CompletionsResponse.
func (p *Proxy) translateResponse(chatResp *ChatCompletionsResponse) *CompletionsResponse {
	if len(chatResp.Choices) == 0 {
		return &CompletionsResponse{
			ID:      chatResp.ID,
			Object:  "text_completion",
			Created: chatResp.Created,
			Model:   chatResp.Model,
			Choices: []CompletionChoice{},
			Usage: CompletionUsage{
				PromptTokens:     chatResp.Usage.PromptTokens,
				CompletionTokens: chatResp.Usage.CompletionTokens,
				TotalTokens:      chatResp.Usage.TotalTokens,
			},
		}
	}

	choice := chatResp.Choices[0]
	text := stripMarkdown(choice.Message.Content)
	return &CompletionsResponse{
		ID:      chatResp.ID,
		Object:  "text_completion",
		Created: chatResp.Created,
		Model:   chatResp.Model,
		Choices: []CompletionChoice{
			{
				Text:         text,
				Index:        0,
				FinishReason: choice.FinishReason,
			},
		},
		Usage: CompletionUsage{
			PromptTokens:     chatResp.Usage.PromptTokens,
			CompletionTokens: chatResp.Usage.CompletionTokens,
			TotalTokens:      chatResp.Usage.TotalTokens,
		},
	}
}

// handleCompletions handles POST /v1/completions requests from Zed.
func (p *Proxy) handleCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	start := time.Now()

	// Limit request body size to 1 MB
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	// Read request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		p.logger.Error("failed to read request body", "error", err)
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	// Parse Zed's CompletionsRequest
	var compReq CompletionsRequest
	if err := json.Unmarshal(body, &compReq); err != nil {
		p.logger.Error("failed to parse request", "error", err)
		http.Error(w, "Invalid request format", http.StatusBadRequest)
		return
	}

	p.logger.Info("request received",
		"method", r.Method,
		"path", r.URL.Path,
		"model", compReq.Model,
		"prompt_length", len(compReq.Prompt),
	)

	// Translate to ChatCompletionsRequest
	chatReq := p.translateRequest(&compReq)

	// Marshal the chat request
	chatBody, err := json.Marshal(chatReq)
	if err != nil {
		p.logger.Error("failed to marshal chat request", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Create context with timeout
	ctx, cancel := context.WithTimeout(r.Context(), p.timeout)
	defer cancel()

	// Create HTTP request to OpenCode Go
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(chatBody))
	if err != nil {
		p.logger.Error("failed to create upstream request", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	// Forward Authorization header from Zed's request
	if auth := r.Header.Get("Authorization"); auth != "" {
		httpReq.Header.Set("Authorization", auth)
	}

	// Send request to OpenCode Go
	resp, err := p.client.Do(httpReq)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			p.logger.Error("upstream request timed out", "timeout", p.timeout)
			http.Error(w, "Gateway Timeout", http.StatusGatewayTimeout)
			return
		}
		p.logger.Error("upstream request failed", "error", err)
		http.Error(w, "Bad Gateway: failed to reach upstream", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Read response body (limited to 1 MB)
	limitedReader := io.LimitReader(resp.Body, 1<<20)
	respBody, err := io.ReadAll(limitedReader)
	if err != nil {
		p.logger.Error("failed to read upstream response", "error", err)
		http.Error(w, "Failed to read upstream response", http.StatusBadGateway)
		return
	}

	// Log raw upstream response for debugging
	p.logger.Info("raw upstream response",
		"status", resp.StatusCode,
		"body_preview", func() string {
			preview := string(respBody)
			if len(preview) > 500 {
				preview = preview[:500] + "..."
			}
			return preview
		}(),
	)

	// If upstream returned an error, pass it through
	if resp.StatusCode != http.StatusOK {
		p.logger.Error("upstream returned error",
			"status", resp.StatusCode,
			"body", string(respBody),
		)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(respBody)
		return
	}

	// Parse ChatCompletionsResponse
	var chatResp ChatCompletionsResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		p.logger.Error("failed to parse upstream response", "error", err)
		http.Error(w, "Invalid upstream response", http.StatusBadGateway)
		return
	}

	// Translate to CompletionsResponse
	compResp := p.translateResponse(&chatResp)

	// Log the actual completion text for debugging
	completionText := ""
	if len(compResp.Choices) > 0 {
		completionText = compResp.Choices[0].Text
	}
	// Truncate for logging
	logText := completionText
	if len(logText) > 200 {
		logText = logText[:200] + "..."
	}
	p.logger.Info("completion details",
		"text_length", len(completionText),
		"text_preview", logText,
		"finish_reason", func() string {
			if len(compResp.Choices) > 0 {
				return compResp.Choices[0].FinishReason
			}
			return "unknown"
		}(),
	)

	// Marshal the completion response
	compBody, err := json.Marshal(compResp)
	if err != nil {
		p.logger.Error("failed to marshal completion response", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	duration := time.Since(start)
	p.logger.Info("response sent",
		"status", http.StatusOK,
		"duration_ms", duration.Milliseconds(),
		"response_length", len(compBody),
	)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(compBody)
}

// handleHealth handles GET /health requests.
func (p *Proxy) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}

func main() {
	// Configure structured JSON logging
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	// Read configuration from environment
	endpoint := os.Getenv("OPENCODE_ENDPOINT")
	if endpoint == "" {
		endpoint = "https://opencode.ai/zen/go/v1/chat/completions"
	}

	model := os.Getenv("MODEL")
	if model == "" {
		model = "deepseek-v4-flash"
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "11111"
	}

	timeoutSec := 10
	if t := os.Getenv("TIMEOUT"); t != "" {
		if v, err := strconv.Atoi(t); err == nil && v > 0 {
			timeoutSec = v
		}
	}

	proxy := NewProxy(endpoint, model, timeoutSec, logger)

	// Set up HTTP server
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/completions", proxy.handleCompletions)
	mux.HandleFunc("/health", proxy.handleHealth)

	server := &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  time.Duration(timeoutSec+5) * time.Second,
		WriteTimeout: time.Duration(timeoutSec+5) * time.Second,
	}

	// Start server in a goroutine
	go func() {
		logger.Info("starting proxy server",
			"port", port,
			"endpoint", endpoint,
			"model", model,
			"timeout", timeoutSec,
		)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server failed to start", "error", err)
			os.Exit(1)
		}
	}()

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("shutting down server...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("server forced to shutdown", "error", err)
		os.Exit(1)
	}

	logger.Info("server exited")
}
