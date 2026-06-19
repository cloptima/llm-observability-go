package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	llmobservability "github.com/cloptima/llm-observability-go"
)

type fakeHTTPClient struct {
	do func(*http.Request) (*http.Response, error)
}

func (f fakeHTTPClient) Do(req *http.Request) (*http.Response, error) {
	return f.do(req)
}

func jsonResponse(statusCode int) *http.Response {
	return &http.Response{
		StatusCode: statusCode,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("{}")),
	}
}

func main() {
	client := llmobservability.InitFromEnv(llmobservability.InitOptions{
		Env: map[string]string{
			"CLOPTIMA_LLM_OBSERVABILITY_API_KEY":     "cloptima_pat_example",
			"CLOPTIMA_LLM_OBSERVABILITY_APP_ID":      "support-api",
			"CLOPTIMA_LLM_OBSERVABILITY_ENVIRONMENT": "dev",
		},
		HTTPClient: fakeHTTPClient{do: func(req *http.Request) (*http.Response, error) {
			var payload map[string]any
			if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
				panic(err)
			}
			if payload["provider"] != "anthropic" || payload["input_tokens"].(float64) != 14 {
				panic("unexpected Anthropic telemetry payload")
			}
			return jsonResponse(http.StatusAccepted), nil
		}},
	})

	_, err := llmobservability.ObserveCall(context.Background(), client, llmobservability.ObserveCallOptions[map[string]any]{
		Provider:      "anthropic",
		Model:         "claude-haiku-4.5",
		ExtractUsage:  llmobservability.ExtractAnthropicUsage,
		FireAndForget: llmobservability.Bool(false),
		Call: func() (map[string]any, error) {
			return map[string]any{
				"id":    "msg-anthropic-example",
				"model": "claude-haiku-4.5",
				"usage": map[string]any{
					"input_tokens":  14,
					"output_tokens": 6,
				},
			}, nil
		},
	})
	if err != nil {
		panic(err)
	}
}
