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
			"CLOPTIMA_LLM_OBSERVABILITY_API_KEY":              "cloptima_pat_example",
			"CLOPTIMA_LLM_OBSERVABILITY_APP_ID":               "support-api",
			"CLOPTIMA_LLM_OBSERVABILITY_ENVIRONMENT":          "dev",
			"CLOPTIMA_LLM_OBSERVABILITY_DELIVERY_MODE":        "otlp_http",
			"CLOPTIMA_LLM_OBSERVABILITY_OTLP_SERVICE_NAME":    "agent-api",
			"CLOPTIMA_LLM_OBSERVABILITY_OTLP_SERVICE_VERSION": "2026.06.14",
		},
		HTTPClient: fakeHTTPClient{do: func(req *http.Request) (*http.Response, error) {
			var payload map[string]any
			if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
				panic(err)
			}
			if req.URL.Path != llmobservability.OTLPTracesPath {
				panic("unexpected OTLP endpoint")
			}
			span := payload["resourceSpans"].([]any)[0].(map[string]any)["scopeSpans"].([]any)[0].(map[string]any)["spans"].([]any)[0].(map[string]any)
			if span["name"] != "llm.openai.gpt-4.1-mini" {
				panic("unexpected OTLP telemetry payload")
			}
			return jsonResponse(http.StatusAccepted), nil
		}},
	})

	previewPayload, err := llmobservability.PreviewEventPayload(llmobservability.UsageEvent{
		Provider:     "openai",
		Model:        "gpt-4.1-mini",
		InputTokens:  llmobservability.Int(10),
		OutputTokens: llmobservability.Int(5),
		Attribution: llmobservability.Attribution{
			FeatureID: "customer_summary",
		},
	}, llmobservability.PreviewOptions{
		DefaultAttribution: llmobservability.Attribution{
			AppID:       "support-api",
			Environment: "dev",
		},
		SDKVersion: llmobservability.PackageVersion,
	})
	if err != nil {
		panic(err)
	}
	if !llmobservability.ValidatePayload(previewPayload).Valid {
		panic("preview payload should be valid")
	}
	previewRequest := llmobservability.PreviewOTLPRequest(previewPayload, llmobservability.OTLPPayloadOptions{
		SDKVersion:     llmobservability.PackageVersion,
		ServiceName:    "agent-api",
		ServiceVersion: "2026.06.14",
	})
	if len(previewRequest["resourceSpans"].([]map[string]any)) == 0 {
		panic("preview request should include resource spans")
	}

	ctx := llmobservability.WithWorkflow(context.Background(), "support_agent", llmobservability.Attribution{
		TeamID: "customer-support",
	})
	_, err = llmobservability.ObserveCall(ctx, client, llmobservability.ObserveCallOptions[map[string]any]{
		Provider:      "openai",
		Model:         "gpt-4.1-mini",
		ExtractUsage:  llmobservability.ExtractOpenAIUsage,
		FireAndForget: llmobservability.Bool(false),
		Attribution: llmobservability.Attribution{
			FeatureID: "customer_summary",
		},
		Metadata: map[string]any{
			"integration_mode": "otlp_http",
		},
		Call: func() (map[string]any, error) {
			return map[string]any{
				"id":    "chatcmpl-otlp-example",
				"model": "gpt-4.1-mini",
				"usage": map[string]any{
					"prompt_tokens":     10,
					"completion_tokens": 5,
					"total_tokens":      15,
				},
			}, nil
		},
	})
	if err != nil {
		panic(err)
	}
}
