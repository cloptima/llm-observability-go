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
			metadata := payload["metadata"].(map[string]any)
			if metadata["workflow_id"] != "support_agent" || metadata["feature_id"] != "draft_reply" {
				panic("expected workflow and task attribution")
			}
			if metadata["team_id"] != "customer-support" || metadata["tenant_id"] != "acme-prod" {
				panic("expected contextual attribution metadata")
			}
			return jsonResponse(http.StatusAccepted), nil
		}},
	})

	ctx := llmobservability.WithWorkflow(context.Background(), "support_agent", llmobservability.Attribution{
		TenantID: "acme-prod",
	})
	ctx = llmobservability.WithTask(ctx, "draft_reply", llmobservability.Attribution{
		TeamID: "customer-support",
	})

	_, err := llmobservability.ObserveCall(ctx, client, llmobservability.ObserveCallOptions[map[string]any]{
		Provider:     "openai",
		Model:        "gpt-4.1-mini",
		ExtractUsage: llmobservability.ExtractOpenAIUsage,
		Call: func() (map[string]any, error) {
			return map[string]any{
				"id":    "chatcmpl-context-example",
				"model": "gpt-4.1-mini",
				"usage": map[string]any{
					"prompt_tokens":     8,
					"completion_tokens": 4,
					"total_tokens":      12,
				},
			}, nil
		},
		FireAndForget: llmobservability.Bool(false),
	})
	if err != nil {
		panic(err)
	}
}
