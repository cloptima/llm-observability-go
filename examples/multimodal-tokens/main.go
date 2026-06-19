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
			units := payload["extra_usage_units"].(map[string]any)
			if units["input_audio"].(float64) != 4 || units["input_image"].(float64) != 3 {
				panic("expected multimodal input units")
			}
			if units["output_image"].(float64) != 2 || units["output_video"].(float64) != 5 {
				panic("expected multimodal output units")
			}
			return jsonResponse(http.StatusAccepted), nil
		}},
	})

	_, err := llmobservability.ObserveCall(context.Background(), client, llmobservability.ObserveCallOptions[map[string]any]{
		Provider:      "openai",
		Model:         "gpt-4.1-mini",
		ExtractUsage:  llmobservability.ExtractOpenAIUsage,
		FireAndForget: llmobservability.Bool(false),
		Call: func() (map[string]any, error) {
			return map[string]any{
				"id":    "chatcmpl-multimodal-example",
				"model": "gpt-4.1-mini",
				"usage": map[string]any{
					"prompt_tokens":     15,
					"completion_tokens": 9,
					"total_tokens":      24,
					"prompt_tokens_details": map[string]any{
						"audio_tokens": 4,
						"image_tokens": 3,
					},
					"completion_tokens_details": map[string]any{
						"image_tokens": 2,
						"video_tokens": 5,
					},
				},
			}, nil
		},
	})
	if err != nil {
		panic(err)
	}
}
