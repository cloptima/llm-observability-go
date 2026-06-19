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
			if payload["provider"] != "gemini" || payload["total_tokens"].(float64) != 21 {
				panic("unexpected Gemini telemetry payload")
			}
			return jsonResponse(http.StatusAccepted), nil
		}},
	})

	_, err := llmobservability.ObserveCall(context.Background(), client, llmobservability.ObserveCallOptions[map[string]any]{
		Provider:      "gemini",
		Model:         "gemini-2.5-flash",
		ExtractUsage:  llmobservability.ExtractGeminiUsage,
		FireAndForget: llmobservability.Bool(false),
		Call: func() (map[string]any, error) {
			return map[string]any{
				"responseId":   "gemini-basic-example",
				"modelVersion": "gemini-2.5-flash",
				"usageMetadata": map[string]any{
					"promptTokenCount":     13,
					"candidatesTokenCount": 8,
					"totalTokenCount":      21,
				},
			}, nil
		},
	})
	if err != nil {
		panic(err)
	}
}
