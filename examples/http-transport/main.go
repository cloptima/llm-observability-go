package main

import (
	"bytes"
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

type fakeRoundTripper struct {
	roundTrip func(*http.Request) (*http.Response, error)
}

func (f fakeRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f.roundTrip(req)
}

func jsonResponse(statusCode int, body string) *http.Response {
	return &http.Response{
		StatusCode: statusCode,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func main() {
	var telemetry map[string]any
	client := llmobservability.InitFromEnv(llmobservability.InitOptions{
		Env: map[string]string{
			"CLOPTIMA_LLM_OBSERVABILITY_API_KEY":     "cloptima_pat_example",
			"CLOPTIMA_LLM_OBSERVABILITY_APP_ID":      "support-api",
			"CLOPTIMA_LLM_OBSERVABILITY_ENVIRONMENT": "dev",
		},
		HTTPClient: fakeHTTPClient{do: func(req *http.Request) (*http.Response, error) {
			if err := json.NewDecoder(req.Body).Decode(&telemetry); err != nil {
				panic(err)
			}
			return jsonResponse(http.StatusAccepted, "{}"), nil
		}},
	})

	baseClient := &http.Client{
		Transport: fakeRoundTripper{roundTrip: func(req *http.Request) (*http.Response, error) {
			if req.URL.Host != "api.openai.com" {
				panic("unexpected provider host")
			}
			return jsonResponse(http.StatusOK, `{"id":"chatcmpl-transport-example","model":"gpt-4o-mini","usage":{"prompt_tokens":11,"completion_tokens":6,"total_tokens":17}}`), nil
		}},
	}
	httpClient := llmobservability.InstrumentHTTPClient(baseClient, client, llmobservability.TransportInstrumentationOptions{
		RequestOptions: llmobservability.TransportRequestOptions{
			Provider:      "openai",
			Model:         "gpt-4o-mini",
			FireAndForget: llmobservability.Bool(false),
			Metadata:      map[string]any{"integration_mode": "shared_transport"},
		},
	})

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://api.openai.com/v1/responses", bytes.NewBufferString(`{"input":"hello"}`))
	if err != nil {
		panic(err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		panic(err)
	}
	if !strings.Contains(string(body), "gpt-4o-mini") {
		panic("expected provider response body")
	}
	if telemetry["provider_request_id"] != "chatcmpl-transport-example" {
		panic("expected provider request id in telemetry")
	}
	metadata := telemetry["metadata"].(map[string]any)
	if metadata["integration_mode"] != "shared_transport" || metadata["response_json_parsed"] != true {
		panic("unexpected transport metadata")
	}
}
