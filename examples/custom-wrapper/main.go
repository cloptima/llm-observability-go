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

type SummaryService struct{}

func (s *SummaryService) GenerateSummary(prompt string) (map[string]any, error) {
	return map[string]any{
		"id":    "chatcmpl-wrapper-example",
		"model": "gpt-4.1-mini",
		"input": prompt,
		"usage": map[string]any{
			"prompt_tokens":     12,
			"completion_tokens": 7,
			"total_tokens":      19,
		},
	}, nil
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
			metadata := payload["metadata"].(map[string]any)
			if metadata["integration_mode"] != "shared_service" {
				panic("unexpected integration mode")
			}
			if metadata["prompt_length"].(float64) <= 0 {
				panic("expected prompt length metadata")
			}
			return jsonResponse(http.StatusAccepted), nil
		}},
	})

	service := &SummaryService{}
	observeGenerate := llmobservability.CreateObservedCall(context.Background(), client, llmobservability.ObserveCallOptions[map[string]any]{
		Provider:      "openai",
		Model:         "gpt-4.1-mini",
		ExtractUsage:  llmobservability.ExtractOpenAIUsage,
		Metadata:      map[string]any{"integration_mode": "shared_service"},
		FireAndForget: llmobservability.Bool(false),
	})

	prompt := "Summarize the customer thread."
	_, err := observeGenerate(func() (map[string]any, error) {
		return service.GenerateSummary(prompt)
	}, &llmobservability.ObserveCallOptions[map[string]any]{
		Attribution: llmobservability.Attribution{
			FeatureID: "support_summary",
		},
		Metadata: map[string]any{
			"prompt_length": len(prompt),
		},
	})
	if err != nil {
		panic(err)
	}
}
