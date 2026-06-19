package llmobservability

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

type TransportRequestOptions struct {
	Provider        string
	Model           string
	ExtractUsage    UsageExtractor
	Attribution     Attribution
	Agent           AgentContext
	Metadata        map[string]any
	MetadataPrivacy *MetadataPrivacyOptions
	RequestID       string
	TraceID         string
	FireAndForget   *bool
}

type TransportInstrumentationOptions struct {
	RequestOptions         TransportRequestOptions
	IncludeHeaders         []string
	RequestIDHeader        string
	TraceIDHeader          string
	OnInstrumentationError func(error)
	ResolveOptions         func(*http.Request) *TransportRequestOptions
}

type instrumentedRoundTripper struct {
	base                   http.RoundTripper
	client                 Client
	requestOptions         TransportRequestOptions
	includeHeaders         []string
	requestIDHeader        string
	traceIDHeader          string
	onInstrumentationError func(error)
	resolveOptions         func(*http.Request) *TransportRequestOptions
}

func InstrumentRoundTripper(base http.RoundTripper, client Client, options TransportInstrumentationOptions) http.RoundTripper {
	if client == nil || !client.IsEnabled() {
		return base
	}
	if base == nil {
		base = http.DefaultTransport
	}
	return &instrumentedRoundTripper{
		base:                   base,
		client:                 client,
		requestOptions:         options.RequestOptions,
		includeHeaders:         append([]string(nil), options.IncludeHeaders...),
		requestIDHeader:        options.RequestIDHeader,
		traceIDHeader:          options.TraceIDHeader,
		onInstrumentationError: options.OnInstrumentationError,
		resolveOptions:         options.ResolveOptions,
	}
}

func InstrumentHTTPClient(httpClient *http.Client, client Client, options TransportInstrumentationOptions) *http.Client {
	if client == nil || !client.IsEnabled() {
		return httpClient
	}
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	cloned := *httpClient
	cloned.Transport = InstrumentRoundTripper(httpClient.Transport, client, options)
	return &cloned
}

func (i *instrumentedRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	options := i.resolveRequestOptions(request)
	if strings.TrimSpace(options.Provider) == "" {
		if i.onInstrumentationError != nil {
			i.onInstrumentationError(errors.New("instrumented HTTP transport requires a provider"))
		}
		return i.base.RoundTrip(request)
	}
	started := time.Now().UTC()
	startedMonotonic := time.Now()
	response, err := i.base.RoundTrip(request)
	completed := time.Now().UTC()
	latency := int(time.Since(startedMonotonic).Milliseconds())
	if latency < 0 {
		latency = 0
	}
	if err != nil {
		_ = recordObservedEvent(request.Context(), i.client, UsageEvent{
			Provider:     options.Provider,
			Model:        fallbackString(options.Model, "unknown"),
			RequestID:    options.RequestID,
			TraceID:      options.TraceID,
			Status:       "failed",
			StartedAt:    &started,
			CompletedAt:  &completed,
			LatencyMS:    Int(latency),
			ErrorMessage: err.Error(),
			Attribution:  options.Attribution,
			Agent:        options.Agent,
			Metadata:     cloneAnyMap(options.Metadata),
		}, options.MetadataPrivacy, resolveBoolOption(options.FireAndForget, true))
		return nil, err
	}

	bodyBytes, parsed, payload := []byte(nil), false, any(nil)
	if !isStreamingHTTPResponse(response) {
		bodyBytes, parsed, payload = readAndRestoreResponseBody(response)
	}
	extracted := ExtractedUsage{}
	extractor := options.ExtractUsage
	if extractor == nil {
		extractor = GetProviderUsageExtractor(options.Provider)
	}
	if extractor != nil && parsed {
		extracted = safeExtract(extractor, payload)
	}
	event := UsageEvent{
		Provider:    fallbackString(options.Provider, "openai"),
		Model:       fallbackString(options.Model, "unknown"),
		RequestID:   options.RequestID,
		TraceID:     options.TraceID,
		Status:      "succeeded",
		StartedAt:   &started,
		CompletedAt: &completed,
		LatencyMS:   Int(latency),
		Attribution: options.Attribution,
		Agent:       options.Agent,
		Metadata:    mergeMetadata(options.Metadata, transportMetadata(request, response, parsed, i.includeHeaders)),
	}
	if response.StatusCode >= http.StatusBadRequest {
		event.Status = "failed"
	}
	applyExtractedUsage(&event, extracted)
	if event.ProviderRequestID == "" {
		event.ProviderRequestID = firstResponseHeader(response, "x-request-id", "request-id", "x-amzn-requestid")
	}
	if event.Provider == "" {
		event.Provider = options.Provider
	}
	if event.Model == "" {
		event.Model = options.Model
	}
	if postErr := recordObservedEvent(request.Context(), i.client, event, options.MetadataPrivacy, resolveBoolOption(options.FireAndForget, true)); postErr != nil && i.onInstrumentationError != nil {
		i.onInstrumentationError(postErr)
	}
	_ = bodyBytes
	return response, nil
}

func (i *instrumentedRoundTripper) resolveRequestOptions(request *http.Request) TransportRequestOptions {
	options := i.requestOptions
	if i.resolveOptions != nil {
		if overrides := i.resolveOptions(request); overrides != nil {
			options = mergeTransportRequestOptions(options, *overrides)
		}
	}
	options.Metadata = mergeMetadata(options.Metadata, selectedHeaderMetadata(request.Header, i.includeHeaders))
	if options.RequestID == "" {
		options.RequestID = requestContextRequestID(request.Header, i.requestIDHeader)
	}
	if options.TraceID == "" {
		options.TraceID = requestContextTraceID(request.Header, i.traceIDHeader)
	}
	return options
}

func mergeTransportRequestOptions(base TransportRequestOptions, overlay TransportRequestOptions) TransportRequestOptions {
	if overlay.Provider != "" {
		base.Provider = overlay.Provider
	}
	if overlay.Model != "" {
		base.Model = overlay.Model
	}
	if overlay.ExtractUsage != nil {
		base.ExtractUsage = overlay.ExtractUsage
	}
	base.Attribution = mergeAttribution(base.Attribution, overlay.Attribution)
	base.Agent = mergeAgentContext(base.Agent, overlay.Agent)
	base.Metadata = mergeMetadata(base.Metadata, overlay.Metadata)
	if overlay.MetadataPrivacy != nil {
		base.MetadataPrivacy = overlay.MetadataPrivacy
	}
	if overlay.RequestID != "" {
		base.RequestID = overlay.RequestID
	}
	if overlay.TraceID != "" {
		base.TraceID = overlay.TraceID
	}
	if overlay.FireAndForget != nil {
		base.FireAndForget = Bool(*overlay.FireAndForget)
	}
	return base
}

func selectedHeaderMetadata(headers http.Header, include []string) map[string]any {
	if len(include) == 0 {
		return nil
	}
	result := make(map[string]any)
	for _, header := range include {
		value := strings.TrimSpace(headers.Get(header))
		if value == "" {
			continue
		}
		result["http_header_"+strings.ToLower(strings.ReplaceAll(header, "-", "_"))] = value
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func transportMetadata(request *http.Request, response *http.Response, parsed bool, includeHeaders []string) map[string]any {
	metadata := map[string]any{
		"http_status_code":     response.StatusCode,
		"response_json_parsed": parsed,
		"http_method":          request.Method,
		"user_agent":           emptyToNilString(strings.TrimSpace(request.Header.Get("User-Agent"))),
	}
	if request.URL != nil {
		metadata["http_host"] = request.URL.Host
		metadata["http_path"] = request.URL.Path
		metadata["http_route"] = request.URL.Path
		metadata["provider_endpoint"] = request.URL.String()
	}
	for key, value := range selectedHeaderMetadata(request.Header, includeHeaders) {
		metadata[key] = value
	}
	return metadata
}

func readAndRestoreResponseBody(response *http.Response) ([]byte, bool, any) {
	if response == nil || response.Body == nil {
		return nil, false, nil
	}
	bodyBytes, err := io.ReadAll(response.Body)
	if err != nil {
		response.Body = io.NopCloser(bytes.NewReader(nil))
		return nil, false, nil
	}
	response.Body.Close()
	response.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	var payload any
	if len(bodyBytes) == 0 {
		return bodyBytes, false, nil
	}
	if err := json.Unmarshal(bodyBytes, &payload); err != nil {
		return bodyBytes, false, nil
	}
	return bodyBytes, true, payload
}

func isStreamingHTTPResponse(response *http.Response) bool {
	if response == nil {
		return false
	}
	contentType := strings.ToLower(strings.TrimSpace(response.Header.Get("Content-Type")))
	return strings.Contains(contentType, "text/event-stream") ||
		strings.Contains(contentType, "application/x-ndjson")
}

func firstResponseHeader(response *http.Response, keys ...string) string {
	if response == nil {
		return ""
	}
	for _, key := range keys {
		value := strings.TrimSpace(response.Header.Get(key))
		if value != "" {
			return value
		}
	}
	return ""
}

func fallbackString(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func InstrumentOpenAICompatibleResponse(ctx context.Context, client Client, response any, options TransportRequestOptions) error {
	extractor := ExtractOpenAIUsage
	if normalizeOpenAICompatibleProvider(options.Provider) == "azure_openai" {
		extractor = ExtractAzureOpenAIUsage
	}
	event := UsageEvent{
		Provider:    fallbackString(options.Provider, "openai"),
		Model:       fallbackString(options.Model, "unknown"),
		RequestID:   options.RequestID,
		TraceID:     options.TraceID,
		Status:      "succeeded",
		Attribution: options.Attribution,
		Agent:       options.Agent,
		Metadata:    cloneAnyMap(options.Metadata),
	}
	applyExtractedUsage(&event, extractor(response))
	return recordObservedEvent(ctx, client, event, options.MetadataPrivacy, resolveBoolOption(options.FireAndForget, true))
}

func InstrumentOpenAICompatibleStream(ctx context.Context, client Client, chunks any, options TransportRequestOptions) error {
	extractor := ExtractOpenAIStreamUsage
	if normalizeOpenAICompatibleProvider(options.Provider) == "azure_openai" {
		extractor = func(input any) ExtractedUsage {
			extracted := ExtractOpenAIStreamUsage(input)
			extracted.Provider = stringPtr("azure_openai")
			return extracted
		}
	}
	event := UsageEvent{
		Provider:    fallbackString(options.Provider, "openai"),
		Model:       fallbackString(options.Model, "unknown"),
		RequestID:   options.RequestID,
		TraceID:     options.TraceID,
		Status:      "succeeded",
		Attribution: options.Attribution,
		Agent:       options.Agent,
		Metadata:    mergeMetadata(options.Metadata, map[string]any{"streamed": true}),
	}
	applyExtractedUsage(&event, extractor(chunks))
	return recordObservedEvent(ctx, client, event, options.MetadataPrivacy, resolveBoolOption(options.FireAndForget, true))
}

func normalizeOpenAICompatibleProvider(provider string) string {
	lowered := strings.ToLower(strings.TrimSpace(provider))
	switch lowered {
	case "azure", "azure-openai", "azure_openai":
		return "azure_openai"
	default:
		return "openai"
	}
}
