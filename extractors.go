package llmobservability

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
)

type UsageExtractor func(any) ExtractedUsage

type ProviderUsageExtractorDescriptor struct {
	Provider          string
	Aliases           []string
	ResponseExtractor UsageExtractor
	StreamExtractor   UsageExtractor
}

type ProviderSupportMatrixEntry struct {
	Provider string   `json:"provider"`
	Aliases  []string `json:"aliases"`
	Response bool     `json:"response"`
	Stream   bool     `json:"stream"`
}

type ExtractedUsage struct {
	Provider              *string
	ProviderRequestID     *string
	Model                 *string
	RequestID             *string
	TraceID               *string
	Status                *string
	InputTokens           *int
	OutputTokens          *int
	TotalTokens           *int
	ReasoningTokens       *int
	CachedInputTokens     *int
	LatencyMS             *int
	ExtraUsageUnits       map[string]int
	CacheHit              *bool
	VendorReportedCostUSD any
	ErrorMessage          *string
	Metadata              map[string]any
}

type MappedUsageExtractorConfig struct {
	Defaults        ExtractedUsageDefaults
	Fields          map[string][]string
	NumberFields    map[string][]string
	BooleanFields   map[string][]string
	ExtraUsageUnits map[string][]string
	Metadata        map[string][]string
}

type ExtractedUsageDefaults struct {
	Provider              string
	ProviderRequestID     string
	Model                 string
	RequestID             string
	TraceID               string
	Status                string
	VendorReportedCostUSD any
	ErrorMessage          string
}

var providerUsageExtractors = []ProviderUsageExtractorDescriptor{
	{
		Provider:          "openai",
		Aliases:           []string{"openai"},
		ResponseExtractor: ExtractOpenAIUsage,
		StreamExtractor:   ExtractOpenAIStreamUsage,
	},
	{
		Provider:          "azure_openai",
		Aliases:           []string{"azure_openai", "azure-openai", "azure"},
		ResponseExtractor: ExtractAzureOpenAIUsage,
		StreamExtractor: func(input any) ExtractedUsage {
			extracted := ExtractOpenAIStreamUsage(input)
			extracted.Provider = stringPtr("azure_openai")
			return extracted
		},
	},
	{
		Provider:          "anthropic",
		Aliases:           []string{"anthropic"},
		ResponseExtractor: ExtractAnthropicUsage,
		StreamExtractor:   ExtractAnthropicStreamUsage,
	},
	{
		Provider:          "gemini",
		Aliases:           []string{"gemini"},
		ResponseExtractor: ExtractGeminiUsage,
		StreamExtractor:   ExtractGeminiStreamUsage,
	},
	{
		Provider:          "vertex_ai",
		Aliases:           []string{"vertex_ai", "vertex-ai", "vertex"},
		ResponseExtractor: ExtractVertexUsage,
		StreamExtractor:   ExtractVertexStreamUsage,
	},
	{
		Provider:          "bedrock",
		Aliases:           []string{"bedrock"},
		ResponseExtractor: ExtractBedrockUsage,
		StreamExtractor:   ExtractBedrockStreamUsage,
	},
}

func ProviderUsageExtractors() []ProviderUsageExtractorDescriptor {
	result := make([]ProviderUsageExtractorDescriptor, len(providerUsageExtractors))
	copy(result, providerUsageExtractors)
	return result
}

func ProviderSupportMatrix() []ProviderSupportMatrixEntry {
	result := make([]ProviderSupportMatrixEntry, 0, len(providerUsageExtractors))
	for _, descriptor := range providerUsageExtractors {
		result = append(result, ProviderSupportMatrixEntry{
			Provider: descriptor.Provider,
			Aliases:  append([]string(nil), descriptor.Aliases...),
			Response: descriptor.ResponseExtractor != nil,
			Stream:   descriptor.StreamExtractor != nil,
		})
	}
	return result
}

func GetProviderUsageExtractor(provider string) UsageExtractor {
	return getProviderExtractor(provider, false)
}

func GetProviderStreamUsageExtractor(provider string) UsageExtractor {
	return getProviderExtractor(provider, true)
}

func ListSupportedProviders() []ProviderSupportMatrixEntry {
	return ProviderSupportMatrix()
}

func TryExtractUsage(input any, extractors ...UsageExtractor) ExtractedUsage {
	for _, extractor := range extractors {
		if extractor == nil {
			continue
		}
		extracted := safeExtract(extractor, input)
		if hasMeaningfulExtraction(extracted) {
			return extracted
		}
	}
	return ExtractedUsage{}
}

func ComposeUsageExtractors(extractors ...UsageExtractor) UsageExtractor {
	return func(input any) ExtractedUsage {
		return TryExtractUsage(input, extractors...)
	}
}

type UsageOverrideFunc func(ExtractedUsage, any) ExtractedUsage

func WithUsageOverrides(extractor UsageExtractor, overrides any) UsageExtractor {
	return func(input any) ExtractedUsage {
		extracted := ExtractedUsage{}
		if extractor != nil {
			extracted = extractor(input)
		}
		switch typed := overrides.(type) {
		case nil:
			return extracted
		case UsageOverrideFunc:
			return mergeExtractedUsage(extracted, typed(extracted, input))
		case func(ExtractedUsage, any) ExtractedUsage:
			return mergeExtractedUsage(extracted, typed(extracted, input))
		case ExtractedUsage:
			return mergeExtractedUsage(extracted, typed)
		default:
			return extracted
		}
	}
}

func CreateMappedUsageExtractor(config MappedUsageExtractorConfig) UsageExtractor {
	return func(input any) ExtractedUsage {
		mapping := coerceMap(input)
		result := ExtractedUsage{
			ExtraUsageUnits: make(map[string]int),
			Metadata:        make(map[string]any),
		}
		if strings.TrimSpace(config.Defaults.Provider) != "" {
			result.Provider = stringPtr(config.Defaults.Provider)
		}
		if strings.TrimSpace(config.Defaults.ProviderRequestID) != "" {
			result.ProviderRequestID = stringPtr(config.Defaults.ProviderRequestID)
		}
		if strings.TrimSpace(config.Defaults.Model) != "" {
			result.Model = stringPtr(config.Defaults.Model)
		}
		if strings.TrimSpace(config.Defaults.RequestID) != "" {
			result.RequestID = stringPtr(config.Defaults.RequestID)
		}
		if strings.TrimSpace(config.Defaults.TraceID) != "" {
			result.TraceID = stringPtr(config.Defaults.TraceID)
		}
		if strings.TrimSpace(config.Defaults.Status) != "" {
			result.Status = stringPtr(config.Defaults.Status)
		}
		if config.Defaults.VendorReportedCostUSD != nil {
			result.VendorReportedCostUSD = config.Defaults.VendorReportedCostUSD
		}
		if strings.TrimSpace(config.Defaults.ErrorMessage) != "" {
			result.ErrorMessage = stringPtr(config.Defaults.ErrorMessage)
		}

		assignExtractedString(&result.Provider, mapping, config.Fields["provider"])
		assignExtractedString(&result.ProviderRequestID, mapping, config.Fields["provider_request_id"])
		assignExtractedString(&result.Model, mapping, config.Fields["model"])
		assignExtractedString(&result.RequestID, mapping, config.Fields["request_id"])
		assignExtractedString(&result.TraceID, mapping, config.Fields["trace_id"])
		assignExtractedString(&result.Status, mapping, config.Fields["status"])
		assignExtractedString(&result.ErrorMessage, mapping, config.Fields["error_message"])
		if value, ok := resolveMappedValue(mapping, config.Fields["vendor_reported_cost_usd"]); ok {
			result.VendorReportedCostUSD = value
		}
		assignExtractedInt(&result.InputTokens, mapping, config.NumberFields["input_tokens"])
		assignExtractedInt(&result.OutputTokens, mapping, config.NumberFields["output_tokens"])
		assignExtractedInt(&result.TotalTokens, mapping, config.NumberFields["total_tokens"])
		assignExtractedInt(&result.ReasoningTokens, mapping, config.NumberFields["reasoning_tokens"])
		assignExtractedInt(&result.CachedInputTokens, mapping, config.NumberFields["cached_input_tokens"])
		assignExtractedInt(&result.LatencyMS, mapping, config.NumberFields["latency_ms"])
		assignExtractedBool(&result.CacheHit, mapping, config.BooleanFields["cache_hit"])
		for key, paths := range config.ExtraUsageUnits {
			if value, ok := resolveMappedInt(mapping, paths); ok {
				result.ExtraUsageUnits[key] = value
			}
		}
		for key, paths := range config.Metadata {
			if value, ok := resolveMappedValue(mapping, paths); ok {
				result.Metadata[key] = value
			}
		}
		if result.TotalTokens == nil && (result.InputTokens != nil || result.OutputTokens != nil) {
			result.TotalTokens = Int(valueOrZero(result.InputTokens) + valueOrZero(result.OutputTokens))
		}
		if len(result.ExtraUsageUnits) == 0 {
			result.ExtraUsageUnits = nil
		}
		if len(result.Metadata) == 0 {
			result.Metadata = nil
		}
		return result
	}
}

func ExtractOpenAIUsage(input any) ExtractedUsage {
	mapping := coerceMap(input)
	usage := nestedMap(mapping, "usage")
	promptDetails := firstNestedMap(usage, "prompt_tokens_details", "promptTokensDetails")
	completionDetails := firstNestedMap(usage, "completion_tokens_details", "completionTokensDetails")
	cached := resolveMappedIntOrMinusOne(promptDetails, "cached_tokens")
	result := ExtractedUsage{
		Provider:          stringPtr("openai"),
		ProviderRequestID: nonEmptyString(mapping["id"]),
		Model:             nonEmptyString(mapping["model"]),
		InputTokens:       nonNegativeInt(resolveMappedIntOrMinusOne(usage, "prompt_tokens")),
		OutputTokens:      nonNegativeInt(resolveMappedIntOrMinusOne(usage, "completion_tokens")),
		TotalTokens:       nonNegativeInt(resolveMappedIntOrMinusOne(usage, "total_tokens")),
		ReasoningTokens:   nonNegativeInt(resolveMappedIntOrMinusOne(completionDetails, "reasoning_tokens")),
		CachedInputTokens: nonNegativeInt(cached),
		ExtraUsageUnits:   extractRuntimeExtraUsageUnits(usage, promptDetails, completionDetails),
	}
	if cached > 0 {
		result.CacheHit = Bool(true)
	}
	return stripEmptyExtractedUsage(result)
}

func ExtractOpenAIStreamUsage(input any) ExtractedUsage {
	chunks := coerceSlice(input)
	var lastID *string
	var lastModel *string
	var usageChunk map[string]any
	for _, chunk := range chunks {
		record := coerceMap(chunk)
		if len(record) == 0 {
			continue
		}
		if value := nonEmptyString(record["id"]); value != nil {
			lastID = value
		}
		if value := nonEmptyString(record["model"]); value != nil {
			lastModel = value
		}
		if len(nestedMap(record, "usage")) > 0 {
			usageChunk = record
		}
	}
	if len(usageChunk) == 0 {
		return stripEmptyExtractedUsage(ExtractedUsage{
			Provider:          stringPtr("openai"),
			ProviderRequestID: lastID,
			Model:             lastModel,
		})
	}
	extracted := ExtractOpenAIUsage(usageChunk)
	if extracted.ProviderRequestID == nil {
		extracted.ProviderRequestID = lastID
	}
	if extracted.Model == nil {
		extracted.Model = lastModel
	}
	return stripEmptyExtractedUsage(extracted)
}

func ExtractAzureOpenAIUsage(input any) ExtractedUsage {
	mapping := coerceMap(input)
	extracted := ExtractOpenAIUsage(input)
	extracted.Provider = stringPtr("azure_openai")
	if extracted.Model == nil {
		extracted.Model = firstNonEmptyString(mapping["deployment_name"], mapping["deployment"], mapping["model"])
	}
	return stripEmptyExtractedUsage(extracted)
}

func ExtractAnthropicUsage(input any) ExtractedUsage {
	mapping := coerceMap(input)
	usage := nestedMap(mapping, "usage")
	inputTokens := resolveMappedIntOrMinusOne(usage, "input_tokens")
	outputTokens := resolveMappedIntOrMinusOne(usage, "output_tokens")
	cacheReadTokens := resolveMappedIntOrMinusOne(usage, "cache_read_input_tokens")
	totalTokens := resolveMappedIntOrMinusOne(usage, "total_tokens")
	extra := extractRuntimeExtraUsageUnits(usage)
	addIfPositive(extra, "server_tool_use", resolveMappedIntOrMinusOne(usage, "server_tool_use"))
	return stripEmptyExtractedUsage(ExtractedUsage{
		Provider:          stringPtr("anthropic"),
		ProviderRequestID: nonEmptyString(mapping["id"]),
		Model:             nonEmptyString(mapping["model"]),
		InputTokens:       nonNegativeInt(inputTokens),
		OutputTokens:      nonNegativeInt(outputTokens),
		TotalTokens:       deriveTotalTokens(totalTokens, inputTokens, outputTokens),
		CachedInputTokens: nonNegativeInt(cacheReadTokens),
		ExtraUsageUnits:   extra,
		CacheHit:          boolIfPositive(cacheReadTokens),
	})
}

func ExtractAnthropicStreamUsage(input any) ExtractedUsage {
	chunks := coerceSlice(input)
	var messageID *string
	var model *string
	inputTokens, outputTokens, cacheReadTokens := -1, -1, -1
	lastInputTokens, lastOutputTokens, lastCacheReadTokens := -1, -1, -1
	extra := map[string]int{}
	lastExtra := map[string]int{}
	for _, chunk := range chunks {
		record := coerceMap(chunk)
		if len(record) == 0 {
			continue
		}
		message := nestedMap(record, "message")
		var usage map[string]any
		if len(message) > 0 {
			if value := nonEmptyString(message["id"]); value != nil {
				messageID = value
			}
			if value := nonEmptyString(message["model"]); value != nil {
				model = value
			}
			usage = nestedMap(message, "usage")
		} else {
			usage = nestedMap(record, "usage")
		}
		inputTokens, lastInputTokens = accumulateStreamingCounter(inputTokens, lastInputTokens, resolveMappedIntOrMinusOne(usage, "input_tokens"))
		outputTokens, lastOutputTokens = accumulateStreamingCounter(outputTokens, lastOutputTokens, resolveMappedIntOrMinusOne(usage, "output_tokens"))
		cacheReadTokens, lastCacheReadTokens = accumulateStreamingCounter(cacheReadTokens, lastCacheReadTokens, resolveMappedIntOrMinusOne(usage, "cache_read_input_tokens"))
		chunkExtra := extractRuntimeExtraUsageUnits(usage)
		addIfPositive(chunkExtra, "server_tool_use", resolveMappedIntOrMinusOne(usage, "server_tool_use"))
		accumulateStreamingUsageMap(extra, lastExtra, chunkExtra)
	}
	return stripEmptyExtractedUsage(ExtractedUsage{
		Provider:          stringPtr("anthropic"),
		ProviderRequestID: messageID,
		Model:             model,
		InputTokens:       nonNegativeInt(inputTokens),
		OutputTokens:      nonNegativeInt(outputTokens),
		TotalTokens:       deriveTotalTokens(-1, inputTokens, outputTokens),
		CachedInputTokens: nonNegativeInt(cacheReadTokens),
		ExtraUsageUnits:   emptyUsageMapToNil(extra),
		CacheHit:          boolIfPositive(cacheReadTokens),
	})
}

func ExtractGeminiUsage(input any) ExtractedUsage {
	mapping := coerceMap(input)
	usage := firstNestedMap(mapping, "usageMetadata", "usage_metadata")
	promptDetails := firstNestedValueAsMapOrList(usage, "promptTokensDetails", "prompt_tokens_details", "inputTokensDetails", "input_tokens_details")
	completionDetails := firstNestedValueAsMapOrList(usage, "candidatesTokensDetails", "candidates_tokens_details", "responseTokensDetails", "response_tokens_details", "outputTokensDetails", "output_tokens_details")
	cached := firstMappedInt(usage, "cachedContentTokenCount", "cached_content_token_count")
	return stripEmptyExtractedUsage(ExtractedUsage{
		Provider:          firstNonEmptyString(mapping["provider"], "gemini"),
		ProviderRequestID: firstNonEmptyString(mapping["responseId"], mapping["response_id"], mapping["id"], mapping["name"]),
		Model:             firstNonEmptyString(mapping["modelVersion"], mapping["model_version"], mapping["model"]),
		InputTokens:       nonNegativeInt(firstMappedInt(usage, "promptTokenCount", "prompt_token_count", "inputTokenCount", "input_token_count")),
		OutputTokens:      nonNegativeInt(firstMappedInt(usage, "responseTokenCount", "response_token_count", "candidatesTokenCount", "candidates_token_count", "outputTokenCount", "output_token_count")),
		TotalTokens:       nonNegativeInt(firstMappedInt(usage, "totalTokenCount", "total_token_count")),
		ReasoningTokens:   nonNegativeInt(firstMappedInt(usage, "thoughtsTokenCount", "thoughts_token_count", "reasoningTokenCount", "reasoning_token_count")),
		CachedInputTokens: nonNegativeInt(cached),
		ExtraUsageUnits:   extractRuntimeExtraUsageUnits(usage, promptDetails, completionDetails),
		CacheHit:          boolIfPositive(cached),
	})
}

func ExtractVertexUsage(input any) ExtractedUsage {
	extracted := ExtractGeminiUsage(input)
	extracted.Provider = stringPtr("vertex_ai")
	return stripEmptyExtractedUsage(extracted)
}

func ExtractGeminiStreamUsage(input any) ExtractedUsage {
	chunks := coerceSlice(input)
	var lastID *string
	var lastModel *string
	var usageChunk map[string]any
	for _, chunk := range chunks {
		record := coerceMap(chunk)
		if len(record) == 0 {
			continue
		}
		if value := firstNonEmptyString(record["responseId"], record["response_id"], record["id"], record["name"]); value != nil {
			lastID = value
		}
		if value := firstNonEmptyString(record["modelVersion"], record["model_version"], record["model"]); value != nil {
			lastModel = value
		}
		if len(firstNestedMap(record, "usageMetadata", "usage_metadata")) > 0 {
			usageChunk = record
		}
	}
	if len(usageChunk) == 0 {
		return stripEmptyExtractedUsage(ExtractedUsage{
			Provider:          stringPtr("gemini"),
			ProviderRequestID: lastID,
			Model:             lastModel,
		})
	}
	extracted := ExtractGeminiUsage(usageChunk)
	if extracted.ProviderRequestID == nil {
		extracted.ProviderRequestID = lastID
	}
	if extracted.Model == nil {
		extracted.Model = lastModel
	}
	return stripEmptyExtractedUsage(extracted)
}

func ExtractVertexStreamUsage(input any) ExtractedUsage {
	extracted := ExtractGeminiStreamUsage(input)
	extracted.Provider = stringPtr("vertex_ai")
	return stripEmptyExtractedUsage(extracted)
}

func ExtractBedrockUsage(input any) ExtractedUsage {
	mapping := coerceMap(input)
	usage := nestedMap(mapping, "usage")
	metrics := nestedMap(mapping, "metrics")
	responseMetadata := nestedMap(mapping, "ResponseMetadata")
	promptDetails := firstNestedValueAsMapOrList(usage, "promptTokensDetails", "prompt_tokens_details", "inputTokensDetails", "input_tokens_details")
	completionDetails := firstNestedValueAsMapOrList(usage, "completionTokensDetails", "completion_tokens_details", "outputTokensDetails", "output_tokens_details")
	return stripEmptyExtractedUsage(ExtractedUsage{
		Provider:          stringPtr("bedrock"),
		ProviderRequestID: firstNonEmptyString(mapping["requestId"], mapping["request_id"], responseMetadata["RequestId"]),
		Model:             firstNonEmptyString(mapping["modelId"], mapping["model_id"], mapping["model"]),
		InputTokens:       nonNegativeInt(firstMappedInt(usage, "inputTokens", "input_tokens")),
		OutputTokens:      nonNegativeInt(firstMappedInt(usage, "outputTokens", "output_tokens")),
		TotalTokens:       nonNegativeInt(firstMappedInt(usage, "totalTokens", "total_tokens")),
		LatencyMS:         nonNegativeInt(firstMappedInt(metrics, "latencyMs", "latency_ms")),
		ExtraUsageUnits:   extractRuntimeExtraUsageUnits(usage, promptDetails, completionDetails),
	})
}

func ExtractBedrockStreamUsage(input any) ExtractedUsage {
	chunks := coerceSlice(input)
	var requestID *string
	var model *string
	inputTokens, outputTokens, totalTokens := -1, -1, -1
	lastInputTokens, lastOutputTokens := -1, -1
	extra := map[string]int{}
	lastExtra := map[string]int{}
	sawUsage := false
	for _, chunk := range chunks {
		record := coerceMap(chunk)
		if len(record) == 0 {
			continue
		}
		if value := firstNonEmptyString(record["requestId"], record["request_id"]); value != nil {
			requestID = value
		}
		if value := firstNonEmptyString(record["modelId"], record["model_id"], record["model"]); value != nil {
			model = value
		}
		usage := nestedMap(record, "usage")
		if len(usage) == 0 {
			continue
		}
		inputCount := firstMappedInt(usage, "inputTokens", "input_tokens")
		outputCount := firstMappedInt(usage, "outputTokens", "output_tokens")
		totalCount := firstMappedInt(usage, "totalTokens", "total_tokens")
		promptDetails := firstNestedValueAsMapOrList(usage, "promptTokensDetails", "prompt_tokens_details", "inputTokensDetails", "input_tokens_details")
		completionDetails := firstNestedValueAsMapOrList(usage, "completionTokensDetails", "completion_tokens_details", "outputTokensDetails", "output_tokens_details")
		chunkExtra := extractRuntimeExtraUsageUnits(usage, promptDetails, completionDetails)
		inputTokens, lastInputTokens = accumulateStreamingCounter(inputTokens, lastInputTokens, inputCount)
		outputTokens, lastOutputTokens = accumulateStreamingCounter(outputTokens, lastOutputTokens, outputCount)
		if inputCount >= 0 || outputCount >= 0 || len(chunkExtra) > 0 {
			sawUsage = true
		}
		accumulateStreamingUsageMap(extra, lastExtra, chunkExtra)
		if totalCount >= 0 {
			totalTokens = totalCount
		}
	}
	return stripEmptyExtractedUsage(ExtractedUsage{
		Provider:          stringPtr("bedrock"),
		ProviderRequestID: requestID,
		Model:             model,
		InputTokens:       nonNegativeIntIf(sawUsage, inputTokens),
		OutputTokens:      nonNegativeIntIf(sawUsage, outputTokens),
		TotalTokens:       deriveTotalTokensIf(sawUsage, totalTokens, inputTokens, outputTokens),
		ExtraUsageUnits:   emptyUsageMapToNil(extra),
	})
}

func getProviderExtractor(provider string, stream bool) UsageExtractor {
	lowered := strings.ToLower(strings.TrimSpace(provider))
	if lowered == "" {
		return nil
	}
	for _, descriptor := range providerUsageExtractors {
		for _, alias := range descriptor.Aliases {
			if lowered == alias {
				if stream {
					return descriptor.StreamExtractor
				}
				return descriptor.ResponseExtractor
			}
		}
	}
	return nil
}

func safeExtract(extractor UsageExtractor, input any) (result ExtractedUsage) {
	defer func() {
		if recover() != nil {
			result = ExtractedUsage{}
		}
	}()
	return extractor(input)
}

func hasMeaningfulExtraction(extracted ExtractedUsage) bool {
	return extracted.Provider != nil ||
		extracted.ProviderRequestID != nil ||
		extracted.Model != nil ||
		extracted.InputTokens != nil ||
		extracted.OutputTokens != nil ||
		extracted.TotalTokens != nil ||
		extracted.ReasoningTokens != nil ||
		extracted.CachedInputTokens != nil ||
		extracted.CacheHit != nil ||
		len(extracted.ExtraUsageUnits) > 0 ||
		len(extracted.Metadata) > 0 ||
		extracted.VendorReportedCostUSD != nil ||
		extracted.LatencyMS != nil
}

func mergeExtractedUsage(base ExtractedUsage, override ExtractedUsage) ExtractedUsage {
	if override.Provider != nil {
		base.Provider = override.Provider
	}
	if override.ProviderRequestID != nil {
		base.ProviderRequestID = override.ProviderRequestID
	}
	if override.Model != nil {
		base.Model = override.Model
	}
	if override.RequestID != nil {
		base.RequestID = override.RequestID
	}
	if override.TraceID != nil {
		base.TraceID = override.TraceID
	}
	if override.Status != nil {
		base.Status = override.Status
	}
	if override.InputTokens != nil {
		base.InputTokens = override.InputTokens
	}
	if override.OutputTokens != nil {
		base.OutputTokens = override.OutputTokens
	}
	if override.TotalTokens != nil {
		base.TotalTokens = override.TotalTokens
	}
	if override.ReasoningTokens != nil {
		base.ReasoningTokens = override.ReasoningTokens
	}
	if override.CachedInputTokens != nil {
		base.CachedInputTokens = override.CachedInputTokens
	}
	if override.LatencyMS != nil {
		base.LatencyMS = override.LatencyMS
	}
	if override.CacheHit != nil {
		base.CacheHit = override.CacheHit
	}
	if override.VendorReportedCostUSD != nil {
		base.VendorReportedCostUSD = override.VendorReportedCostUSD
	}
	if override.ErrorMessage != nil {
		base.ErrorMessage = override.ErrorMessage
	}
	if len(override.ExtraUsageUnits) > 0 {
		if base.ExtraUsageUnits == nil {
			base.ExtraUsageUnits = map[string]int{}
		}
		for key, value := range override.ExtraUsageUnits {
			base.ExtraUsageUnits[key] = value
		}
	}
	if len(override.Metadata) > 0 {
		if base.Metadata == nil {
			base.Metadata = map[string]any{}
		}
		for key, value := range override.Metadata {
			base.Metadata[key] = value
		}
	}
	return stripEmptyExtractedUsage(base)
}

func stripEmptyExtractedUsage(value ExtractedUsage) ExtractedUsage {
	if len(value.ExtraUsageUnits) == 0 {
		value.ExtraUsageUnits = nil
	}
	if len(value.Metadata) == 0 {
		value.Metadata = nil
	}
	return value
}

func coerceMap(input any) map[string]any {
	switch typed := input.(type) {
	case map[string]any:
		return typed
	}
	if input == nil {
		return map[string]any{}
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return map[string]any{}
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return map[string]any{}
	}
	return decoded
}

func coerceSlice(input any) []any {
	switch typed := input.(type) {
	case []any:
		return typed
	}
	value := reflect.ValueOf(input)
	if !value.IsValid() {
		return nil
	}
	if value.Kind() != reflect.Slice && value.Kind() != reflect.Array {
		return nil
	}
	result := make([]any, 0, value.Len())
	for index := 0; index < value.Len(); index++ {
		result = append(result, value.Index(index).Interface())
	}
	return result
}

func nestedMap(mapping map[string]any, key string) map[string]any {
	if value, ok := mapping[key].(map[string]any); ok {
		return value
	}
	return map[string]any{}
}

func firstNestedMap(mapping map[string]any, keys ...string) map[string]any {
	for _, key := range keys {
		if value, ok := mapping[key].(map[string]any); ok {
			return value
		}
	}
	return map[string]any{}
}

func firstNestedValueAsMapOrList(mapping map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := mapping[key]; ok {
			return value
		}
	}
	return nil
}

func assignExtractedString(target **string, mapping map[string]any, paths []string) {
	if value, ok := resolveMappedString(mapping, paths); ok {
		*target = stringPtr(value)
	}
}

func assignExtractedInt(target **int, mapping map[string]any, paths []string) {
	if value, ok := resolveMappedInt(mapping, paths); ok {
		*target = Int(value)
	}
}

func assignExtractedBool(target **bool, mapping map[string]any, paths []string) {
	if value, ok := resolveMappedBool(mapping, paths); ok {
		*target = Bool(value)
	}
}

func resolveMappedValue(mapping map[string]any, paths []string) (any, bool) {
	for _, path := range paths {
		value, ok := pathValue(mapping, path)
		if ok && value != nil && value != "" {
			return value, true
		}
	}
	return nil, false
}

func resolveMappedString(mapping map[string]any, paths []string) (string, bool) {
	value, ok := resolveMappedValue(mapping, paths)
	if !ok {
		return "", false
	}
	text := strings.TrimSpace(asString(value))
	return text, text != ""
}

func resolveMappedInt(mapping map[string]any, paths []string) (int, bool) {
	value, ok := resolveMappedValue(mapping, paths)
	if !ok {
		return 0, false
	}
	number := resolveInt(value)
	return number, number >= 0
}

func resolveMappedBool(mapping map[string]any, paths []string) (bool, bool) {
	value, ok := resolveMappedValue(mapping, paths)
	if !ok {
		return false, false
	}
	switch typed := value.(type) {
	case bool:
		return typed, true
	case string:
		return parseBool(typed)
	default:
		return false, false
	}
}

func pathValue(mapping map[string]any, path string) (any, bool) {
	current := any(mapping)
	for _, segment := range strings.Split(path, ".") {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			continue
		}
		nested, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = nested[segment]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func resolveInt(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case float32:
		return int(typed)
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return int(parsed)
		}
		if parsed, err := typed.Float64(); err == nil {
			return int(parsed)
		}
	case string:
		if parsed, ok := parseIntegerString(typed); ok {
			return parsed
		}
	}
	return -1
}

func parseIntegerString(value string) (int, bool) {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err == nil {
		return parsed, true
	}
	return 0, false
}

func nonEmptyString(value any) *string {
	text := strings.TrimSpace(asString(value))
	if text == "" || text == "<nil>" {
		return nil
	}
	return &text
}

func firstNonEmptyString(values ...any) *string {
	for _, value := range values {
		if resolved := nonEmptyString(value); resolved != nil {
			return resolved
		}
	}
	return nil
}

func nonNegativeInt(value int) *int {
	if value < 0 {
		return nil
	}
	return Int(value)
}

func nonNegativeIntIf(enabled bool, value int) *int {
	if !enabled {
		return nil
	}
	return nonNegativeInt(value)
}

func deriveTotalTokens(totalTokens int, inputTokens int, outputTokens int) *int {
	if totalTokens >= 0 {
		return Int(totalTokens)
	}
	if inputTokens >= 0 || outputTokens >= 0 {
		return Int(maxZero(inputTokens) + maxZero(outputTokens))
	}
	return nil
}

func deriveTotalTokensIf(enabled bool, totalTokens int, inputTokens int, outputTokens int) *int {
	if !enabled {
		return nil
	}
	return deriveTotalTokens(totalTokens, inputTokens, outputTokens)
}

func maxZero(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

func boolIfPositive(value int) *bool {
	if value > 0 {
		return Bool(true)
	}
	return nil
}

func addIfPositive(target map[string]int, key string, value int) {
	if value >= 0 {
		target[key] = value
	}
}

func emptyUsageMapToNil(value map[string]int) map[string]int {
	if len(value) == 0 {
		return nil
	}
	return value
}

func stringPtr(value string) *string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func resolveMappedIntOrMinusOne(mapping map[string]any, key string) int {
	if mapping == nil {
		return -1
	}
	return resolveInt(mapping[key])
}

func firstMappedInt(mapping map[string]any, keys ...string) int {
	for _, key := range keys {
		if value, ok := mapping[key]; ok {
			if resolved := resolveInt(value); resolved >= 0 {
				return resolved
			}
		}
	}
	return -1
}

func extractRuntimeExtraUsageUnits(values ...any) map[string]int {
	result := map[string]int{}
	for index, value := range values {
		switch typed := value.(type) {
		case map[string]any:
			if index == 1 {
				addIfPositive(result, "input_audio", firstMappedInt(typed, "audio_tokens", "audioTokenCount", "audio_token_count"))
				addIfPositive(result, "input_image", firstMappedInt(typed, "image_tokens", "imageTokenCount", "image_token_count"))
				addIfPositive(result, "input_video", firstMappedInt(typed, "video_tokens", "videoTokenCount", "video_token_count"))
			} else if index == 2 {
				addIfPositive(result, "output_audio", firstMappedInt(typed, "audio_tokens", "audioTokenCount", "audio_token_count"))
				addIfPositive(result, "output_image", firstMappedInt(typed, "image_tokens", "imageTokenCount", "image_token_count"))
				addIfPositive(result, "output_video", firstMappedInt(typed, "video_tokens", "videoTokenCount", "video_token_count"))
			}
			addIfPositive(result, "input_audio", firstMappedInt(typed, "input_audio_tokens", "inputAudioTokens"))
			addIfPositive(result, "input_image", firstMappedInt(typed, "input_image_tokens", "inputImageTokens"))
			addIfPositive(result, "input_video", firstMappedInt(typed, "input_video_tokens", "inputVideoTokens"))
			addIfPositive(result, "output_audio", firstMappedInt(typed, "output_audio_tokens", "outputAudioTokens"))
			addIfPositive(result, "output_image", firstMappedInt(typed, "output_image_tokens", "outputImageTokens"))
			addIfPositive(result, "output_video", firstMappedInt(typed, "output_video_tokens", "outputVideoTokens"))
			addIfPositive(result, "cache_write", firstMappedInt(typed, "cache_creation_input_tokens", "cacheCreationInputTokens"))
		case []any:
			for _, item := range typed {
				entry := coerceMap(item)
				modality := strings.ToLower(strings.TrimSpace(asString(firstNonEmptyValue(entry["modality"], entry["type"]))))
				count := firstMappedInt(entry, "tokenCount", "token_count")
				switch modality {
				case "audio":
					addIfPositive(result, inferModalityDirection(index, entry, "audio"), count)
				case "image":
					addIfPositive(result, inferModalityDirection(index, entry, "image"), count)
				case "video":
					addIfPositive(result, inferModalityDirection(index, entry, "video"), count)
				}
			}
		}
	}
	return emptyUsageMapToNil(result)
}

func inferModalityDirection(index int, entry map[string]any, modality string) string {
	label := strings.ToLower(strings.TrimSpace(asString(firstNonEmptyValue(entry["direction"], entry["role"], entry["kind"], entry["bucket"]))))
	switch label {
	case "output", "response", "candidate", "completion":
		return "output_" + modality
	case "input", "prompt", "request":
		return "input_" + modality
	}
	switch index {
	case 2:
		return "output_" + modality
	case 1:
		return "input_" + modality
	default:
		return "input_" + modality
	}
}

func firstNonEmptyValue(values ...any) any {
	for _, value := range values {
		if strings.TrimSpace(asString(value)) != "" && asString(value) != "<nil>" {
			return value
		}
	}
	return nil
}

func accumulateStreamingCounter(current int, previous int, next int) (int, int) {
	if next < 0 {
		return current, previous
	}
	if current < 0 {
		return next, next
	}
	if previous < 0 {
		if next > current {
			return next, next
		}
		return current + next, next
	}
	if next >= previous {
		return current + (next - previous), next
	}
	return current + next, next
}

func accumulateStreamingUsageMap(current map[string]int, previous map[string]int, next map[string]int) {
	for key, value := range next {
		if value < 0 {
			continue
		}
		if prior, ok := previous[key]; ok {
			if value >= prior {
				current[key] += value - prior
			} else {
				current[key] += value
			}
		} else {
			current[key] += value
		}
		previous[key] = value
	}
}
