package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// ProviderID identifies the OpenAI-compatible provider.
	ProviderID = "openai-compatible"
)

// Provider defines the interface for completing chat requests.
type Provider interface {
	Complete(ctx context.Context, req ChatRequest) (ChatResponse, error)
}

// ChatRequest represents a chat completion request.
type ChatRequest struct {
	System    string
	User      string
	Model     string
	MaxTokens int
}

// ChatResponse represents a chat completion response.
type ChatResponse struct {
	Content string
	Model   string
}

// OpenAICompatibleClient implements Provider using OpenAI-compatible API.
type OpenAICompatibleClient struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	model      string
	maxTokens  int
}

// NewClient creates a new OpenAI-compatible client.
func NewClient(baseURL, apiKey, model string, timeout time.Duration, maxTokens int) *OpenAICompatibleClient {
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	return &OpenAICompatibleClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: timeout},
		model:      model,
		maxTokens:  maxTokens,
	}
}

// Complete sends a chat completion request.
func (c *OpenAICompatibleClient) Complete(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	model := req.Model
	if model == "" {
		model = c.model
	}
	if model == "" {
		return ChatResponse{}, errors.New("model is required")
	}

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = c.maxTokens
	}

	messages := make([]map[string]string, 0, 2)
	if req.System != "" {
		messages = append(messages, map[string]string{"role": "system", "content": req.System})
	}
	if req.User == "" {
		return ChatResponse{}, errors.New("user content is required")
	}
	messages = append(messages, map[string]string{"role": "user", "content": req.User})

	payload := map[string]any{
		"model":      model,
		"messages":   messages,
		"stream":     false,
		"max_tokens": maxTokens,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("marshal request: %w", err)
	}

	url := c.baseURL + "/chat/completions"
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return ChatResponse{}, fmt.Errorf("invalid base URL: %s", c.baseURL)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return ChatResponse{}, fmt.Errorf("provider returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var parsed struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return ChatResponse{}, fmt.Errorf("unmarshal response: %w", err)
	}
	if len(parsed.Choices) == 0 || strings.TrimSpace(parsed.Choices[0].Message.Content) == "" {
		return ChatResponse{}, errors.New("malformed provider response: empty content")
	}

	return ChatResponse{Content: parsed.Choices[0].Message.Content, Model: parsed.Model}, nil
}
