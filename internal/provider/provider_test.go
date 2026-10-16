package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type chatRequestPayload struct {
	Model    string `json:"model"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
	Stream    bool `json:"stream"`
	MaxTokens int  `json:"max_tokens"`
}

func writeChatResponse(t *testing.T, w http.ResponseWriter, model, content string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	response := map[string]any{
		"model": model,
		"choices": []map[string]any{
			{"message": map[string]any{"role": "assistant", "content": content}},
		},
	}
	if err := json.NewEncoder(w).Encode(response); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

func TestCompleteSendsExpectedRequestAndParsesResponse(t *testing.T) {
	var got chatRequestPayload
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %s, want /chat/completions", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		writeChatResponse(t, w, "served-model", "hello from model")
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-api-key-12345", "default-model", 5*time.Second, 256)
	response, err := client.Complete(context.Background(), ChatRequest{
		System: "system instructions",
		User:   "user task",
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if response.Content != "hello from model" || response.Model != "served-model" {
		t.Fatalf("unexpected response: %#v", response)
	}
	if got.Model != "default-model" {
		t.Fatalf("request model = %q, want default-model", got.Model)
	}
	if got.MaxTokens != 256 {
		t.Fatalf("request max_tokens = %d, want 256", got.MaxTokens)
	}
	if got.Stream {
		t.Fatalf("request stream = true, want false")
	}
	if len(got.Messages) != 2 ||
		got.Messages[0].Role != "system" || got.Messages[0].Content != "system instructions" ||
		got.Messages[1].Role != "user" || got.Messages[1].Content != "user task" {
		t.Fatalf("unexpected messages: %#v", got.Messages)
	}
	if gotAuth != "Bearer test-api-key-12345" {
		t.Fatalf("authorization header = %q", gotAuth)
	}
}

func TestCompleteHonorsRequestOverrides(t *testing.T) {
	var got chatRequestPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		writeChatResponse(t, w, "override-model", "ok")
	}))
	defer server.Close()

	client := NewClient(server.URL, "", "default-model", 5*time.Second, 256)
	if _, err := client.Complete(context.Background(), ChatRequest{
		User:      "task",
		Model:     "override-model",
		MaxTokens: 999,
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if got.Model != "override-model" {
		t.Fatalf("request model = %q, want override-model", got.Model)
	}
	if got.MaxTokens != 999 {
		t.Fatalf("request max_tokens = %d, want 999", got.MaxTokens)
	}
	if len(got.Messages) != 1 || got.Messages[0].Role != "user" {
		t.Fatalf("system message should be omitted when empty: %#v", got.Messages)
	}
}

func TestCompleteOmitsAuthorizationWhenNoKey(t *testing.T) {
	var gotAuth string
	var hadAuth bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, hadAuth = r.Header["Authorization"]
		writeChatResponse(t, w, "m", "ok")
	}))
	defer server.Close()

	client := NewClient(server.URL, "", "m", 5*time.Second, 64)
	if _, err := client.Complete(context.Background(), ChatRequest{User: "task"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if hadAuth || gotAuth != "" {
		t.Fatalf("unexpected authorization header: %q", gotAuth)
	}
}

func TestCompleteRejectsErrorStatuses(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusInternalServerError, http.StatusBadGateway} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte("provider failure detail"))
			}))
			defer server.Close()

			client := NewClient(server.URL, "", "m", 5*time.Second, 64)
			_, err := client.Complete(context.Background(), ChatRequest{User: "task"})
			if err == nil {
				t.Fatalf("expected error for status %d", status)
			}
			if !strings.Contains(err.Error(), "provider failure detail") {
				t.Fatalf("error does not include provider detail: %v", err)
			}
		})
	}
}

func TestCompleteErrorsDoNotExposeAPIKey(t *testing.T) {
	const apiKey = "sk-super-secret-key-do-not-log"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal error"))
	}))
	defer server.Close()

	client := NewClient(server.URL, apiKey, "m", 5*time.Second, 64)
	_, err := client.Complete(context.Background(), ChatRequest{User: "task"})
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), apiKey) {
		t.Fatalf("error exposed API key: %v", err)
	}
}

func TestCompleteConnectionRefused(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := server.URL
	server.Close()

	client := NewClient(url, "", "m", 2*time.Second, 64)
	if _, err := client.Complete(context.Background(), ChatRequest{User: "task"}); err == nil {
		t.Fatal("expected connection error after server close")
	}
}

func TestCompleteTimesOut(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer func() {
		close(release)
		server.Close()
	}()

	client := NewClient(server.URL, "", "m", 50*time.Millisecond, 64)
	start := time.Now()
	_, err := client.Complete(context.Background(), ChatRequest{User: "task"})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("timeout took too long: %s", elapsed)
	}
}

func TestCompleteRespectsContextCancellation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
	}))
	defer func() {
		close(release)
		server.Close()
	}()

	client := NewClient(server.URL, "", "m", 5*time.Second, 64)
	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		_, err := client.Complete(ctx, ChatRequest{User: "task"})
		errCh <- err
	}()

	<-started
	cancel()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected cancellation error")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Complete did not return after context cancellation")
	}
}

func TestCompleteRejectsMalformedResponses(t *testing.T) {
	tests := map[string]string{
		"malformed json":          `{"model":`,
		"empty choices":           `{"choices":[]}`,
		"empty content":           `{"choices":[{"message":{"content":""}}]}`,
		"missing message":         `{"choices":[{}]}`,
		"whitespace-only content": `{"choices":[{"message":{"content":"   "}}]}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()

			client := NewClient(server.URL, "", "m", 5*time.Second, 64)
			if _, err := client.Complete(context.Background(), ChatRequest{User: "task"}); err == nil {
				t.Fatalf("expected error for body %q", body)
			}
		})
	}
}

func TestCompleteRequiresUserContent(t *testing.T) {
	client := NewClient("http://127.0.0.1:1/v1", "", "m", time.Second, 64)
	if _, err := client.Complete(context.Background(), ChatRequest{System: "system"}); err == nil {
		t.Fatal("expected error when user content is empty")
	}
}
