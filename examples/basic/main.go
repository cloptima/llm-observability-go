package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	llmobservability "github.com/cloptima/llm-observability-go"
)

const defaultIngestURL = "https://api.cloptima.ai/v1/ai/integrations/sdk/events"

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
			"CLOPTIMA_LLM_OBSERVABILITY_TEAM_ID":     "customer-support",
		},
		HTTPClient: fakeHTTPClient{do: func(req *http.Request) (*http.Response, error) {
			var payload map[string]any
			if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
				panic(err)
			}
			if req.URL.String() != defaultIngestURL || req.Header.Get("Authorization") == "" {
				panic("unexpected telemetry request")
			}
			if payload["provider"] != "openai" || payload["model"] != "gpt-4.1-mini" {
				panic("unexpected telemetry payload")
			}
			metadata := payload["metadata"].(map[string]any)
			if metadata["integration_mode"] != "direct_sdk" {
				panic("unexpected integration mode")
			}
			return jsonResponse(http.StatusAccepted), nil
		}},
	})

	_, err := llmobservability.ObserveCall(context.Background(), client, llmobservability.ObserveCallOptions[map[string]any]{
		Provider:     "openai",
		Model:        "gpt-4.1-mini",
		ExtractUsage: llmobservability.ExtractOpenAIUsage,
		Call: func() (map[string]any, error) {
			return map[string]any{
				"id":    "chatcmpl-example",
				"model": "gpt-4.1-mini",
				"usage": map[string]any{
					"prompt_tokens":     10,
					"completion_tokens": 5,
					"total_tokens":      15,
				},
			}, nil
		},
		Attribution: llmobservability.Attribution{
			FeatureID: "summary_generation",
		},
		Metadata:      map[string]any{"integration_mode": "direct_sdk"},
		FireAndForget: llmobservability.Bool(false),
	})
	if err != nil {
		panic(err)
	}
}
