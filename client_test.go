package llmobservability

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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
		Body:       io.NopCloser(strings.NewReader(`{}`)),
		Header:     make(http.Header),
	}
}

type fakeRoundTripper struct {
	roundTrip func(*http.Request) (*http.Response, error)
}

func (f fakeRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f.roundTrip(req)
}

type closeOnlyClient struct {
	closeCalls atomic.Int64
	timeout    time.Duration
}

func (c *closeOnlyClient) IsEnabled() bool                          { return true }
func (c *closeOnlyClient) InitError() error                         { return nil }
func (c *closeOnlyClient) Record(context.Context, UsageEvent) error { return nil }
func (c *closeOnlyClient) RecordBatch(context.Context, []UsageEvent) error {
	return nil
}
func (c *closeOnlyClient) RecordAsync(context.Context, UsageEvent) {}
func (c *closeOnlyClient) Stats() Stats                            { return Stats{} }
func (c *closeOnlyClient) Flush(time.Duration) bool                { return true }
func (c *closeOnlyClient) Close(timeout time.Duration) bool {
	c.timeout = timeout
	c.closeCalls.Add(1)
	return true
}

func TestInitFromEnvReturnsDisabledClientWhenNotConfigured(t *testing.T) {
	client := InitFromEnv(InitOptions{Env: map[string]string{}, Strict: true})

	if client.IsEnabled() {
		t.Fatal("expected disabled client")
	}
	if IsEnabled(InitOptions{Env: map[string]string{}}) {
		t.Fatal("expected IsEnabled to be false")
	}
	if err := client.Record(context.Background(), UsageEvent{Provider: "openai", Model: "gpt-4.1-mini"}); err != nil {
		t.Fatalf("disabled record should be silent: %v", err)
	}
}

func TestInitFromEnvBuildsConfiguredClientAndPostsRecord(t *testing.T) {
	var mu sync.Mutex
	var path string
	var auth string
	var userAgent string
	var body map[string]any
	httpClient := fakeHTTPClient{do: func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		path = r.URL.Path
		auth = r.Header.Get("Authorization")
		userAgent = r.Header.Get("User-Agent")
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		return jsonResponse(http.StatusAccepted), nil
	}}

	client := InitFromEnv(InitOptions{
		Env: map[string]string{
			InitAPIBaseURLEnv:  "https://sdk-ingest.example.cloptima.ai",
			InitAPIKeyEnv:      "pat-env",
			InitAppIDEnv:       "agent-api",
			InitEnvironmentEnv: "prod",
			InitTeamIDEnv:      "platform",
		},
		Strict:     true,
		HTTPClient: httpClient,
	})

	if !client.IsEnabled() {
		t.Fatal("expected enabled client")
	}
	err := client.Record(context.Background(), UsageEvent{
		Provider:    "openai",
		Model:       "gpt-4.1-mini",
		InputTokens: Int(4),
	})
	if err != nil {
		t.Fatalf("record failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if path != SDKIngestPath {
		t.Fatalf("expected ingest path %s, got %s", SDKIngestPath, path)
	}
	if auth != "Bearer pat-env" {
		t.Fatalf("unexpected authorization: %s", auth)
	}
	if !strings.HasPrefix(userAgent, "cloptima-llm-observability-go/") {
		t.Fatalf("unexpected user-agent: %s", userAgent)
	}
	metadata := body["metadata"].(map[string]any)
	if metadata["app_id"] != "agent-api" {
		t.Fatalf("expected app_id")
	}
	if metadata["environment"] != "prod" {
		t.Fatalf("expected environment")
	}
	if metadata["team_id"] != "platform" {
		t.Fatalf("expected team_id")
	}
}

func TestSchemeLessLocalhostBaseURLUsesHTTP(t *testing.T) {
	client, err := NewClient(Config{
		APIBaseURL: "127.0.0.1:4318",
		APIKey:     "pat",
		DefaultAttribution: Attribution{
			AppID:       "agent-api",
			Environment: "dev",
		},
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	concrete := client.(*observabilityClient)
	if concrete.ingestURL != "http://127.0.0.1:4318"+SDKIngestPath {
		t.Fatalf("unexpected ingest url: %s", concrete.ingestURL)
	}
	if concrete.otlpURL != "http://127.0.0.1:4318"+OTLPTracesPath {
		t.Fatalf("unexpected otlp url: %s", concrete.otlpURL)
	}
}

func TestPreviewPayloadAndValidation(t *testing.T) {
	started := time.Now().UTC()
	payload, err := PreviewEventPayload(UsageEvent{
		Provider:     "openai",
		Model:        "gpt-4.1-mini",
		InputTokens:  Int(3),
		OutputTokens: Int(5),
		StartedAt:    &started,
	}, PreviewOptions{
		DefaultAttribution: Attribution{
			AppID:       "agent-api",
			Environment: "dev",
		},
		SDKVersion: PackageVersion,
	})
	if err != nil {
		t.Fatalf("preview event: %v", err)
	}
	if payload["schema_version"] != SDKEventSchemaVersion {
		t.Fatalf("unexpected schema version")
	}
	if payload["total_tokens"] != 8 {
		t.Fatalf("expected total tokens to be derived")
	}
	validation := ValidatePayload(payload)
	if !validation.Valid {
		t.Fatalf("expected valid payload, got %v", validation.Errors)
	}
	invalid := ValidatePayload(map[string]any{
		"schema_version": "wrong",
		"sdk_name":       "",
		"provider":       "openai",
		"model":          "gpt-4.1-mini",
		"metadata":       map[string]any{},
	})
	if invalid.Valid {
		t.Fatal("expected invalid payload")
	}
	keys := StrictFinopsMetadataKeys()
	if len(keys) != 20 {
		t.Fatalf("expected 20 strict finops keys, got %d", len(keys))
	}
	if !slices.Contains(keys, "route") || !slices.Contains(keys, "trace_id") {
		t.Fatalf("expected strict finops helper to expose stable keys, got %#v", keys)
	}
}

func TestSourceEventIDFallsBackToRequestIdentifiers(t *testing.T) {
	defaults := PreviewOptions{
		DefaultAttribution: Attribution{
			AppID:       "agent-api",
			Environment: "dev",
		},
	}
	testCases := []struct {
		name     string
		event    UsageEvent
		expected string
	}{
		{
			name: "source_event_id wins",
			event: UsageEvent{
				Provider:      "openai",
				Model:         "gpt-4o-mini",
				SourceEventID: "event-explicit-1",
				RequestID:     "request-derive-1",
			},
			expected: "event-explicit-1",
		},
		{
			name: "request_id fallback",
			event: UsageEvent{
				Provider:  "openai",
				Model:     "gpt-4o-mini",
				RequestID: "request-derive-1",
			},
			expected: "request-derive-1",
		},
		{
			name: "provider_request_id fallback",
			event: UsageEvent{
				Provider:          "openai",
				Model:             "gpt-4o-mini",
				ProviderRequestID: "provider-derive-1",
			},
			expected: "provider-derive-1",
		},
		{
			name: "trace_id fallback",
			event: UsageEvent{
				Provider: "openai",
				Model:    "gpt-4o-mini",
				TraceID:  "trace-derive-1",
			},
			expected: "trace-derive-1",
		},
	}
	for _, tc := range testCases {
		payload, err := PreviewEventPayload(tc.event, defaults)
		if err != nil {
			t.Fatalf("%s: preview event: %v", tc.name, err)
		}
		if payload["source_event_id"] != tc.expected {
			t.Fatalf("%s: expected source_event_id %q, got %v", tc.name, tc.expected, payload["source_event_id"])
		}
	}

	generatedPayload, err := PreviewEventPayload(UsageEvent{
		Provider: "openai",
		Model:    "gpt-4o-mini",
	}, defaults)
	if err != nil {
		t.Fatalf("preview generated event: %v", err)
	}
	sourceEventID, _ := generatedPayload["source_event_id"].(string)
	if !strings.HasPrefix(sourceEventID, "clop_evt_") {
		t.Fatalf("expected generated source_event_id prefix, got %q", sourceEventID)
	}
}

func TestOTLPModePostsOtlpRequest(t *testing.T) {
	var body map[string]any
	var path string
	httpClient := fakeHTTPClient{do: func(r *http.Request) (*http.Response, error) {
		path = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		return jsonResponse(http.StatusAccepted), nil
	}}

	client, err := NewClient(Config{
		APIBaseURL: "https://sdk-ingest.example.cloptima.ai",
		APIKey:     "pat",
		DefaultAttribution: Attribution{
			AppID:       "checkout-api",
			Environment: "dev",
		},
		DeliveryMode:       DeliveryModeOTLPHTTP,
		OTLPServiceVersion: "2026.06.1",
		SDKVersion:         PackageVersion,
		HTTPClient:         httpClient,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if err := client.Record(context.Background(), UsageEvent{
		Provider:        "openai",
		Model:           "gpt-4o-mini",
		ExtraUsageUnits: map[string]int{"input_image": 3, "output_audio": 5},
	}); err != nil {
		t.Fatalf("record failed: %v", err)
	}
	if path != OTLPTracesPath {
		t.Fatalf("expected OTLP path, got %s", path)
	}
	resourceSpans := body["resourceSpans"].([]any)
	resource := resourceSpans[0].(map[string]any)
	attributes := resource["resource"].(map[string]any)["attributes"].([]any)
	if attributes[0].(map[string]any)["value"].(map[string]any)["stringValue"] != "cloptima-llm-observability-go" {
		t.Fatalf("expected default service name")
	}
	spanAttrs := otlpAttrMap(resource["scopeSpans"].([]any)[0].(map[string]any)["spans"].([]any)[0].(map[string]any)["attributes"].([]any))
	if otlpIntValue(spanAttrs["gen_ai.usage.input_image"]["intValue"]) != 3 {
		t.Fatalf("expected input image OTLP usage unit, got %#v", spanAttrs["gen_ai.usage.input_image"])
	}
	if otlpIntValue(spanAttrs["gen_ai.usage.output_audio"]["intValue"]) != 5 {
		t.Fatalf("expected output audio OTLP usage unit, got %#v", spanAttrs["gen_ai.usage.output_audio"])
	}
	if spanAttrs["extra_usage_units"]["stringValue"] != "{\"input_image\":3,\"output_audio\":5}" {
		t.Fatalf("expected serialized extra usage units, got %#v", spanAttrs["extra_usage_units"])
	}
}

func TestRecordAsyncFlushAndStats(t *testing.T) {
	var bodies []map[string]any
	var mu sync.Mutex
	httpClient := fakeHTTPClient{do: func(r *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		return jsonResponse(http.StatusAccepted), nil
	}}

	client, err := NewClient(Config{
		APIBaseURL:         "https://sdk-ingest.example.cloptima.ai",
		APIKey:             "pat",
		AsyncBatchSize:     2,
		AsyncFlushInterval: 10 * time.Millisecond,
		DefaultAttribution: Attribution{
			AppID:       "agent-api",
			Environment: "dev",
		},
		HTTPClient: httpClient,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	client.RecordAsync(context.Background(), UsageEvent{Provider: "openai", Model: "gpt-4o-mini"})
	client.RecordAsync(context.Background(), UsageEvent{Provider: "openai", Model: "gpt-4o"})
	if !client.Flush(2 * time.Second) {
		t.Fatal("flush timed out")
	}
	stats := client.Stats()
	if stats.DeliveredEvents != 2 {
		t.Fatalf("expected 2 delivered events, got %+v", stats)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 1 {
		t.Fatalf("expected one batch payload, got %d", len(bodies))
	}
	if bodies[0]["schema_version"] != SDKBatchSchemaVersion {
		t.Fatalf("expected batch schema version")
	}
}

func TestObserveCallRecordsSuccessAndFailure(t *testing.T) {
	var statuses []string
	var mu sync.Mutex
	httpClient := fakeHTTPClient{do: func(r *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		mu.Lock()
		statuses = append(statuses, body["status"].(string))
		mu.Unlock()
		return jsonResponse(http.StatusAccepted), nil
	}}

	client, err := NewClient(Config{
		APIBaseURL: "https://sdk-ingest.example.cloptima.ai",
		APIKey:     "pat",
		DefaultAttribution: Attribution{
			AppID:       "agent-api",
			Environment: "dev",
		},
		HTTPClient: httpClient,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	ctx := WithWorkflow(context.Background(), "support-agent", Attribution{})
	_, err = ObserveCall(ctx, client, ObserveCallOptions[string]{
		Provider: "openai",
		Model:    "gpt-4.1-mini",
		Call: func() (string, error) {
			return "ok", nil
		},
		FireAndForget: Bool(false),
	})
	if err != nil {
		t.Fatalf("observe success: %v", err)
	}
	_, err = ObserveCall(ctx, client, ObserveCallOptions[string]{
		Provider: "openai",
		Model:    "gpt-4.1-mini",
		Call: func() (string, error) {
			return "", errors.New("boom")
		},
		FireAndForget: Bool(false),
	})
	if err == nil {
		t.Fatal("expected error")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(statuses) != 2 || statuses[0] != "succeeded" || statuses[1] != "failed" {
		t.Fatalf("unexpected statuses: %v", statuses)
	}
}

func TestMetadataPrivacyRedactsPrompt(t *testing.T) {
	payload, err := PreviewEventPayload(UsageEvent{
		Provider: "openai",
		Model:    "gpt-4.1-mini",
		Metadata: map[string]any{
			"prompt": "very secret prompt",
			"route":  "/chat",
		},
	}, PreviewOptions{
		DefaultAttribution: Attribution{
			AppID:       "agent-api",
			Environment: "dev",
		},
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	metadata := payload["metadata"].(map[string]any)
	if metadata["prompt"] != "[redacted]" {
		t.Fatalf("expected redacted prompt, got %v", metadata["prompt"])
	}
}

func TestOpenAIExtractorsFromFixtures(t *testing.T) {
	fixturePath := filepath.Join("..", "llm-observability-fixtures", "provider_usage_replay.json")
	body, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	var fixtures []map[string]any
	if err := json.Unmarshal(body, &fixtures); err != nil {
		t.Fatalf("unmarshal fixtures: %v", err)
	}
	for _, fixture := range fixtures {
		provider := fixture["provider"].(string)
		expected := fixture["expected"].(map[string]any)
		var extractor UsageExtractor
		if fixture["kind"] == "stream" {
			extractor = GetProviderStreamUsageExtractor(provider)
		} else {
			extractor = GetProviderUsageExtractor(provider)
		}
		if extractor == nil {
			t.Fatalf("fixture %s: missing extractor for provider %s", fixture["name"], provider)
		}
		assertExtractedUsageMatches(t, fixture["name"].(string), extractor(fixture["payload"]), expected)
	}
}

func TestTryComposeAndOverrideExtractors(t *testing.T) {
	empty := func(any) ExtractedUsage { return ExtractedUsage{} }
	base := func(any) ExtractedUsage {
		return ExtractedUsage{
			Provider:    stringPtr("openai"),
			Model:       stringPtr("gpt-4o-mini"),
			InputTokens: Int(4),
		}
	}
	extracted := TryExtractUsage(map[string]any{}, empty, base)
	if extracted.Provider == nil || *extracted.Provider != "openai" {
		t.Fatal("expected TryExtractUsage to select meaningful extractor")
	}
	composed := ComposeUsageExtractors(empty, base)
	if composed(nil).Model == nil || *composed(nil).Model != "gpt-4o-mini" {
		t.Fatal("expected composed extractor result")
	}
	overridden := WithUsageOverrides(base, ExtractedUsage{OutputTokens: Int(6)})
	if overridden(nil).OutputTokens == nil || *overridden(nil).OutputTokens != 6 {
		t.Fatal("expected usage overrides to apply")
	}
}

func TestListSupportedProviders(t *testing.T) {
	supported := ListSupportedProviders()
	if len(supported) < 6 {
		t.Fatalf("expected provider support matrix, got %d entries", len(supported))
	}
}

func TestObserveStreamRecordsSuccess(t *testing.T) {
	var body map[string]any
	httpClient := fakeHTTPClient{do: func(r *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		return jsonResponse(http.StatusAccepted), nil
	}}
	client, err := NewClient(Config{
		APIBaseURL: "https://sdk-ingest.example.cloptima.ai",
		APIKey:     "pat",
		DefaultAttribution: Attribution{
			AppID:       "agent-api",
			Environment: "dev",
		},
		HTTPClient: httpClient,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	var seen []string
	err = ObserveStream(context.Background(), client, ObserveStreamOptions[string]{
		Provider: "openai",
		Model:    "gpt-4o-mini",
		Call: func(emit func(string) error) error {
			for _, chunk := range []string{"a", "b"} {
				if err := emit(chunk); err != nil {
					return err
				}
			}
			return nil
		},
		ExtractUsage: func(chunks []string) ExtractedUsage {
			if len(chunks) != 2 {
				t.Fatalf("expected buffered chunks")
			}
			return ExtractedUsage{OutputTokens: Int(2)}
		},
		FireAndForget: Bool(false),
	}, func(chunk string) error {
		seen = append(seen, chunk)
		return nil
	})
	if err != nil {
		t.Fatalf("observe stream: %v", err)
	}
	if len(seen) != 2 {
		t.Fatalf("expected 2 chunks")
	}
	if body["status"] != "succeeded" {
		t.Fatalf("expected succeeded event")
	}
	metadata := body["metadata"].(map[string]any)
	if metadata["streamed"] != true {
		t.Fatalf("expected streamed metadata")
	}
}

func TestObserveStreamDefaultsToFireAndForget(t *testing.T) {
	var delivered atomic.Int64
	httpClient := fakeHTTPClient{do: func(r *http.Request) (*http.Response, error) {
		delivered.Add(1)
		return jsonResponse(http.StatusAccepted), nil
	}}
	client, err := NewClient(Config{
		APIBaseURL: "https://sdk-ingest.example.cloptima.ai",
		APIKey:     "pat",
		DefaultAttribution: Attribution{
			AppID:       "agent-api",
			Environment: "dev",
		},
		HTTPClient:         httpClient,
		AsyncFlushInterval: 0,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	err = ObserveStream(context.Background(), client, ObserveStreamOptions[string]{
		Provider: "openai",
		Model:    "gpt-4o-mini",
		Call: func(emit func(string) error) error {
			return emit("chunk-1")
		},
	}, func(string) error { return nil })
	if err != nil {
		t.Fatalf("observe stream: %v", err)
	}
	if delivered.Load() != 0 {
		t.Fatalf("expected no synchronous delivery before flush")
	}
	if !client.Flush(2 * time.Second) {
		t.Fatalf("expected flush to succeed")
	}
	if delivered.Load() != 1 {
		t.Fatalf("expected async delivery after flush, got %d", delivered.Load())
	}
}

func TestObserveCallIgnoresTelemetryOnlyFailure(t *testing.T) {
	var observed []error
	client, err := NewClient(Config{
		APIBaseURL: "https://sdk-ingest.example.cloptima.ai",
		APIKey:     "pat",
		DefaultAttribution: Attribution{
			AppID:       "checkout-api",
			Environment: "dev",
		},
		HTTPClient: fakeHTTPClient{do: func(*http.Request) (*http.Response, error) {
			return nil, errors.New("ingest unavailable")
		}},
		OnError: func(err error) {
			observed = append(observed, err)
		},
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	result, err := ObserveCall(context.Background(), client, ObserveCallOptions[string]{
		Provider:      "openai",
		Model:         "gpt-4o-mini",
		Call:          func() (string, error) { return "ok", nil },
		FireAndForget: Bool(false),
	})
	if err != nil {
		t.Fatalf("observe call returned telemetry error: %v", err)
	}
	if result != "ok" {
		t.Fatalf("unexpected result: %s", result)
	}
	if len(observed) != 1 || !strings.Contains(observed[0].Error(), "ingest unavailable") {
		t.Fatalf("expected telemetry failure in onError, got %#v", observed)
	}
}

func TestObserveStreamIgnoresTelemetryOnlyFailure(t *testing.T) {
	var observed []error
	client, err := NewClient(Config{
		APIBaseURL: "https://sdk-ingest.example.cloptima.ai",
		APIKey:     "pat",
		DefaultAttribution: Attribution{
			AppID:       "checkout-api",
			Environment: "dev",
		},
		HTTPClient: fakeHTTPClient{do: func(*http.Request) (*http.Response, error) {
			return nil, errors.New("ingest unavailable")
		}},
		OnError: func(err error) {
			observed = append(observed, err)
		},
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	var chunks []string
	err = ObserveStream(context.Background(), client, ObserveStreamOptions[string]{
		Provider:      "openai",
		Model:         "gpt-4o-mini",
		Call:          func(emit func(string) error) error { return emit("chunk-1") },
		FireAndForget: Bool(false),
	}, func(chunk string) error {
		chunks = append(chunks, chunk)
		return nil
	})
	if err != nil {
		t.Fatalf("observe stream returned telemetry error: %v", err)
	}
	if len(chunks) != 1 || chunks[0] != "chunk-1" {
		t.Fatalf("unexpected chunks: %#v", chunks)
	}
	if len(observed) != 1 || !strings.Contains(observed[0].Error(), "ingest unavailable") {
		t.Fatalf("expected telemetry failure in onError, got %#v", observed)
	}
}

func TestInstrumentRoundTripperRecordsResponse(t *testing.T) {
	var ingestBody map[string]any
	ingestClient := fakeHTTPClient{do: func(r *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(r.Body).Decode(&ingestBody); err != nil {
			t.Fatalf("decode ingest body: %v", err)
		}
		return jsonResponse(http.StatusAccepted), nil
	}}
	cloptimaClient, err := NewClient(Config{
		APIBaseURL: "https://sdk-ingest.example.cloptima.ai",
		APIKey:     "pat",
		DefaultAttribution: Attribution{
			AppID:       "agent-api",
			Environment: "dev",
		},
		HTTPClient: ingestClient,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	baseTransport := fakeRoundTripper{roundTrip: func(req *http.Request) (*http.Response, error) {
		payload := `{"id":"chatcmpl-fixture-openai","model":"gpt-4.1-mini","usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewBufferString(payload)),
		}, nil
	}}
	httpClient := &http.Client{
		Transport: InstrumentRoundTripper(baseTransport, cloptimaClient, TransportInstrumentationOptions{
			RequestOptions: TransportRequestOptions{
				Provider:      "openai",
				Model:         "gpt-4.1-mini",
				FireAndForget: Bool(false),
			},
			IncludeHeaders: []string{"X-Trace-Id"},
		}),
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://provider.example/v1/chat/completions", nil)
	req.Header.Set("X-Trace-Id", "trace-123")
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("instrumented request failed: %v", err)
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(bodyBytes), "chatcmpl-fixture-openai") {
		t.Fatalf("expected response body to be restored")
	}
	if ingestBody["provider_request_id"] != "chatcmpl-fixture-openai" {
		t.Fatalf("expected recorded provider request id")
	}
	metadata := ingestBody["metadata"].(map[string]any)
	if metadata["http_header_x_trace_id"] != "trace-123" {
		t.Fatalf("expected selected request header metadata")
	}
}

func TestInstrumentRoundTripperPreservesStreamingBodies(t *testing.T) {
	var ingestBody map[string]any
	ingestClient := fakeHTTPClient{do: func(r *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(r.Body).Decode(&ingestBody); err != nil {
			t.Fatalf("decode ingest body: %v", err)
		}
		return jsonResponse(http.StatusAccepted), nil
	}}
	cloptimaClient, err := NewClient(Config{
		APIBaseURL: "https://sdk-ingest.example.cloptima.ai",
		APIKey:     "pat",
		DefaultAttribution: Attribution{
			AppID:       "agent-api",
			Environment: "dev",
		},
		HTTPClient: ingestClient,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	streamPayload := "data: hello\n\n"
	baseTransport := fakeRoundTripper{roundTrip: func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(bytes.NewBufferString(streamPayload)),
		}, nil
	}}
	httpClient := &http.Client{
		Transport: InstrumentRoundTripper(baseTransport, cloptimaClient, TransportInstrumentationOptions{
			RequestOptions: TransportRequestOptions{
				Provider:      "openai",
				Model:         "gpt-4.1-mini",
				FireAndForget: Bool(false),
			},
		}),
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://provider.example/v1/chat/stream", nil)
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("instrumented streaming request failed: %v", err)
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(resp.Body)
	if string(bodyBytes) != streamPayload {
		t.Fatalf("expected streaming body to remain intact")
	}
	metadata := ingestBody["metadata"].(map[string]any)
	if metadata["response_json_parsed"] != false {
		t.Fatalf("expected streaming transport to skip JSON parsing")
	}
}

func TestRequestContextDataPropagatesIntoObservedCall(t *testing.T) {
	var body map[string]any
	httpClient := fakeHTTPClient{do: func(r *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		return jsonResponse(http.StatusAccepted), nil
	}}
	client, err := NewClient(Config{
		APIBaseURL: "https://sdk-ingest.example.cloptima.ai",
		APIKey:     "pat",
		DefaultAttribution: Attribution{
			AppID:       "agent-api",
			Environment: "dev",
		},
		HTTPClient: httpClient,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	ctx := WithRequestContextData(context.Background(), RequestContextData{
		Attribution: Attribution{WorkflowID: "support-agent"},
		RequestID:   "req-ctx-1",
		TraceID:     "trace-ctx-1",
		Metadata:    map[string]any{"http_route": "/v1/chat"},
	})
	_, err = ObserveCall(ctx, client, ObserveCallOptions[string]{
		Provider: "openai",
		Model:    "gpt-4o-mini",
		Call: func() (string, error) {
			return "ok", nil
		},
		FireAndForget: Bool(false),
	})
	if err != nil {
		t.Fatalf("observe call: %v", err)
	}
	if body["request_id"] != "req-ctx-1" {
		t.Fatalf("expected request_id from context")
	}
	if body["trace_id"] != "trace-ctx-1" {
		t.Fatalf("expected trace_id from context")
	}
	metadata := body["metadata"].(map[string]any)
	if metadata["workflow_id"] != "support-agent" {
		t.Fatalf("expected workflow attribution from context")
	}
	if metadata["http_route"] != "/v1/chat" {
		t.Fatalf("expected metadata from request context")
	}
}

func TestInstrumentHTTPRequestContext(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "https://app.example.dev/v1/chat", nil)
	req.Header.Set("X-Request-Id", "req-123")
	req.Header.Set("Traceparent", "traceparent-123")
	req.Header.Set("User-Agent", "sdk-test")
	req.Header.Set("X-Tenant", "acme")
	req.RemoteAddr = "10.1.2.3:4567"
	data := InstrumentHTTPRequestContext(req, HTTPRequestContextOptions{
		Attribution:    Attribution{TeamID: "support"},
		IncludeHeaders: []string{"X-Tenant"},
		Route:          "/v1/chat",
	})
	if data.RequestID != "req-123" {
		t.Fatalf("expected request id")
	}
	if data.TraceID != "traceparent-123" {
		t.Fatalf("expected traceparent fallback")
	}
	if data.Metadata["client_ip"] != "10.1.2.3" {
		t.Fatalf("expected client ip")
	}
	if data.Metadata["http_header_x_tenant"] != "acme" {
		t.Fatalf("expected selected header metadata")
	}
}

func TestInstrumentOpenAICompatibleHelpers(t *testing.T) {
	var bodies []map[string]any
	httpClient := fakeHTTPClient{do: func(r *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		bodies = append(bodies, body)
		return jsonResponse(http.StatusAccepted), nil
	}}
	client, err := NewClient(Config{
		APIBaseURL: "https://sdk-ingest.example.cloptima.ai",
		APIKey:     "pat",
		DefaultAttribution: Attribution{
			AppID:       "agent-api",
			Environment: "dev",
		},
		HTTPClient: httpClient,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	response := map[string]any{
		"id":    "chatcmpl-openai-helper",
		"model": "gpt-4.1-mini",
		"usage": map[string]any{"prompt_tokens": 2, "completion_tokens": 3, "total_tokens": 5},
	}
	if err := InstrumentOpenAICompatibleResponse(context.Background(), client, response, TransportRequestOptions{FireAndForget: Bool(false)}); err != nil {
		t.Fatalf("response helper: %v", err)
	}
	stream := []any{
		map[string]any{"id": "chatcmpl-openai-helper-stream", "model": "gpt-4.1-mini"},
		map[string]any{"id": "chatcmpl-openai-helper-stream", "usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 2, "total_tokens": 3}},
	}
	if err := InstrumentOpenAICompatibleStream(context.Background(), client, stream, TransportRequestOptions{FireAndForget: Bool(false)}); err != nil {
		t.Fatalf("stream helper: %v", err)
	}
	if len(bodies) != 2 {
		t.Fatalf("expected two recorded helper events")
	}
	if bodies[0]["provider_request_id"] != "chatcmpl-openai-helper" {
		t.Fatalf("expected response helper to record provider request id")
	}
	if bodies[1]["provider_request_id"] != "chatcmpl-openai-helper-stream" {
		t.Fatalf("expected stream helper to record provider request id")
	}
}

func TestRunWithHelpers(t *testing.T) {
	workflowID := RunWithWorkflow(context.Background(), "wf-1", Attribution{}, func(ctx context.Context) string {
		return currentAttribution(ctx).WorkflowID
	})
	if workflowID != "wf-1" {
		t.Fatalf("expected workflow id from helper")
	}
	featureID := RunWithTask(context.Background(), "task-1", Attribution{}, func(ctx context.Context) string {
		return currentAttribution(ctx).FeatureID
	})
	if featureID != "task-1" {
		t.Fatalf("expected feature id from helper")
	}
}

func TestCreateObservedCallOverrideCanDisableFireAndForget(t *testing.T) {
	var body map[string]any
	httpClient := fakeHTTPClient{do: func(r *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		return jsonResponse(http.StatusAccepted), nil
	}}
	client, err := NewClient(Config{
		APIBaseURL: "https://sdk-ingest.example.cloptima.ai",
		APIKey:     "pat",
		DefaultAttribution: Attribution{
			AppID:       "agent-api",
			Environment: "dev",
		},
		HTTPClient: httpClient,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	observed := CreateObservedCall(context.Background(), client, ObserveCallOptions[string]{
		Provider:      "openai",
		Model:         "gpt-4o-mini",
		FireAndForget: Bool(true),
	})
	_, err = observed(func() (string, error) {
		return "ok", nil
	}, &ObserveCallOptions[string]{FireAndForget: Bool(false)})
	if err != nil {
		t.Fatalf("observed call: %v", err)
	}
	if body["provider"] != "openai" {
		t.Fatalf("expected synchronous override to record immediately")
	}
}

func TestRegisterSignalShutdownClosesClient(t *testing.T) {
	client := &closeOnlyClient{}
	var captured chan os.Signal
	originalNotify := signalNotify
	originalStop := signalStop
	signalNotify = func(ch chan os.Signal, _ ...os.Signal) {
		captured = ch
	}
	signalStop = func(chan os.Signal) {}
	defer func() {
		signalNotify = originalNotify
		signalStop = originalStop
	}()

	stop := RegisterSignalShutdown(client, 250*time.Millisecond)
	defer stop()
	if captured == nil {
		t.Fatalf("expected signal hook registration")
	}
	captured <- os.Interrupt

	deadline := time.Now().Add(time.Second)
	for client.closeCalls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if client.closeCalls.Load() != 1 {
		t.Fatalf("expected client close on signal, got %d", client.closeCalls.Load())
	}
	if client.timeout != 250*time.Millisecond {
		t.Fatalf("expected close timeout to propagate, got %s", client.timeout)
	}
}

func TestRegisterSignalShutdownStopCancelsHook(t *testing.T) {
	client := &closeOnlyClient{}
	var captured chan os.Signal
	originalNotify := signalNotify
	originalStop := signalStop
	signalNotify = func(ch chan os.Signal, _ ...os.Signal) {
		captured = ch
	}
	signalStop = func(chan os.Signal) {}
	defer func() {
		signalNotify = originalNotify
		signalStop = originalStop
	}()

	stop := RegisterSignalShutdown(client, time.Second)
	if captured == nil {
		t.Fatalf("expected signal hook registration")
	}
	stop()
	captured <- os.Interrupt
	time.Sleep(25 * time.Millisecond)
	if client.closeCalls.Load() != 0 {
		t.Fatalf("expected stopped hook to suppress close, got %d", client.closeCalls.Load())
	}
}

func assertExtractedUsageMatches(t *testing.T, name string, extracted ExtractedUsage, expected map[string]any) {
	t.Helper()
	if expectedProvider, ok := expected["provider"].(string); ok {
		if extracted.Provider == nil || *extracted.Provider != expectedProvider {
			t.Fatalf("%s: provider mismatch: %#v", name, extracted.Provider)
		}
	}
	if expectedID, ok := expected["provider_request_id"].(string); ok {
		if extracted.ProviderRequestID == nil || *extracted.ProviderRequestID != expectedID {
			t.Fatalf("%s: provider_request_id mismatch: %#v", name, extracted.ProviderRequestID)
		}
	}
	if expectedModel, ok := expected["model"].(string); ok {
		if extracted.Model == nil || *extracted.Model != expectedModel {
			t.Fatalf("%s: model mismatch: %#v", name, extracted.Model)
		}
	}
	assertExpectedIntPtr(t, name, "input_tokens", extracted.InputTokens, expected["input_tokens"])
	assertExpectedIntPtr(t, name, "output_tokens", extracted.OutputTokens, expected["output_tokens"])
	assertExpectedIntPtr(t, name, "total_tokens", extracted.TotalTokens, expected["total_tokens"])
	assertExpectedIntPtr(t, name, "reasoning_tokens", extracted.ReasoningTokens, expected["reasoning_tokens"])
	assertExpectedIntPtr(t, name, "cached_input_tokens", extracted.CachedInputTokens, expected["cached_input_tokens"])
	assertExpectedIntPtr(t, name, "latency_ms", extracted.LatencyMS, expected["latency_ms"])
	if expectedCacheHit, ok := expected["cache_hit"].(bool); ok {
		if extracted.CacheHit == nil || *extracted.CacheHit != expectedCacheHit {
			t.Fatalf("%s: cache_hit mismatch", name)
		}
	}
	if expectedExtra, ok := expected["extra_usage_units"].(map[string]any); ok {
		if len(extracted.ExtraUsageUnits) != len(expectedExtra) {
			t.Fatalf("%s: extra usage length mismatch: %#v", name, extracted.ExtraUsageUnits)
		}
		for key, value := range expectedExtra {
			if extracted.ExtraUsageUnits[key] != int(value.(float64)) {
				t.Fatalf("%s: extra usage mismatch for %s: %#v", name, key, extracted.ExtraUsageUnits)
			}
		}
	}
}

func otlpAttrMap(attributes []any) map[string]map[string]any {
	result := make(map[string]map[string]any, len(attributes))
	for _, raw := range attributes {
		entry := raw.(map[string]any)
		result[entry["key"].(string)] = entry["value"].(map[string]any)
	}
	return result
}

func otlpIntValue(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}

func assertExpectedIntPtr(t *testing.T, name string, field string, actual *int, expected any) {
	t.Helper()
	if expected == nil {
		return
	}
	value := int(expected.(float64))
	if actual == nil || *actual != value {
		t.Fatalf("%s: %s mismatch: %#v", name, field, actual)
	}
}
