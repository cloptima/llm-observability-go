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
			if payload["provider"] != "gemini" {
				panic("unexpected provider")
			}
			if payload["vendor_reported_cost_usd"].(float64) != 0.4321 {
				panic("expected mapped vendor cost")
			}
			extraUsage := payload["extra_usage_units"].(map[string]any)
			if extraUsage["output_image"].(float64) != 96 {
				panic("expected mapped extra usage units")
			}
			return jsonResponse(http.StatusAccepted), nil
		}},
	})

	extractUsage := llmobservability.CreateMappedUsageExtractor(llmobservability.MappedUsageExtractorConfig{
		Defaults: llmobservability.ExtractedUsageDefaults{
			Provider: "gemini",
		},
		Fields: map[string][]string{
			"model":                    {"modelVersion"},
			"provider_request_id":      {"responseId"},
			"vendor_reported_cost_usd": {"billing.costUsd"},
		},
		NumberFields: map[string][]string{
			"input_tokens":  {"usage.promptTokenCount"},
			"output_tokens": {"usage.responseTokenCount"},
			"total_tokens":  {"usage.totalTokenCount"},
		},
		ExtraUsageUnits: map[string][]string{
			"output_image": {"usage.outputImageTokenCount"},
		},
	})

	_, err := llmobservability.ObserveCall(context.Background(), client, llmobservability.ObserveCallOptions[map[string]any]{
		Provider:      "gemini",
		Model:         "gemini-2.5-pro",
		ExtractUsage:  extractUsage,
		FireAndForget: llmobservability.Bool(false),
		Call: func() (map[string]any, error) {
			return map[string]any{
				"responseId":   "gemini-mapped-example",
				"modelVersion": "gemini-2.5-pro",
				"usage": map[string]any{
					"promptTokenCount":      18,
					"responseTokenCount":    7,
					"totalTokenCount":       25,
					"outputImageTokenCount": 96,
				},
				"billing": map[string]any{
					"costUsd": "0.4321",
				},
			}, nil
		},
	})
	if err != nil {
		panic(err)
	}
}
