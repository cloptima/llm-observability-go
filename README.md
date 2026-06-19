# Cloptima LLM Observability Go SDK

Capture LLM usage telemetry from your Go application and send it to Cloptima for cost reporting, attribution, and usage analytics.

This SDK is designed for teams that want observability without replacing their existing provider clients, wrappers, retries, auth, or application security controls.

## Install

```bash
go get github.com/cloptima/llm-observability-go
```

## Quick start

Required configuration:

- `CLOPTIMA_LLM_OBSERVABILITY_API_KEY`
- `CLOPTIMA_LLM_OBSERVABILITY_APP_ID`

Recommended while testing:

- `CLOPTIMA_LLM_OBSERVABILITY_ENVIRONMENT=dev`

```go
package main

import (
	"context"
	"time"

	llmobservability "github.com/cloptima/llm-observability-go"
)

func main() {
	client := llmobservability.InitFromEnv(llmobservability.InitOptions{})
	defer client.Close(2 * time.Second)
	stop := llmobservability.RegisterSignalShutdown(client, 2*time.Second)
	defer stop()

	_, _ = llmobservability.ObserveCall(context.Background(), client, llmobservability.ObserveCallOptions[map[string]any]{
		Provider: "openai",
		Model:    "gpt-4.1-mini",
		Call: func() (map[string]any, error) {
			return summaryServiceGenerate(), nil
		},
		ExtractUsage: llmobservability.ExtractOpenAIUsage,
		Attribution: llmobservability.Attribution{
			FeatureID:  "summary_generation",
			WorkflowID: "support_agent",
		},
		FireAndForget: llmobservability.Bool(false),
	})
}

func summaryServiceGenerate() map[string]any {
	return map[string]any{
		"id":    "chatcmpl-example",
		"model": "gpt-4.1-mini",
		"usage": map[string]any{
			"prompt_tokens":     10,
			"completion_tokens": 5,
			"total_tokens":      15,
		},
	}
}
```

By default, the SDK sends bearer-authenticated HTTPS requests to Cloptima at `https://api.cloptima.ai/v1/ai/integrations/sdk/events`.

If the required configuration is missing, `InitFromEnv(...)` returns a disabled pass-through client so local development and tests do not break.

For short-lived processes, call `defer client.Close(...)` so buffered fire-and-forget telemetry gets a chance to flush. `RegisterSignalShutdown(...)` is available when you also want SIGINT and SIGTERM to trigger the same close path.

## Choose your integration path

### Call-site or wrapper boundary

This is the default path for most teams.

Use it when you already know the provider, model, and business context at the point where your code calls an LLM or an existing AI wrapper.

- `ObserveCall(...)` for direct integration
- `CreateObservedCall(...)` for reusable wrappers
- `BindObservedCall(...)` for existing zero-argument closures

```go
package main

import (
	"context"

	llmobservability "github.com/cloptima/llm-observability-go"
)

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
	client := llmobservability.InitFromEnv(llmobservability.InitOptions{})
	service := &SummaryService{}
	observeGenerate := llmobservability.CreateObservedCall(context.Background(), client, llmobservability.ObserveCallOptions[map[string]any]{
		Provider:        "openai",
		Model:           "gpt-4.1-mini",
		ExtractUsage:    llmobservability.ExtractOpenAIUsage,
		FireAndForget:   llmobservability.Bool(false),
		Metadata:        map[string]any{"integration_mode": "shared_service"},
	})

	prompt := "Summarize the customer thread."
	_, _ = observeGenerate(func() (map[string]any, error) {
		return service.GenerateSummary(prompt)
	}, &llmobservability.ObserveCallOptions[map[string]any]{
		Attribution: llmobservability.Attribution{
			FeatureID: "support_summary",
		},
		Metadata: map[string]any{
			"prompt_length": len(prompt),
		},
	})
}
```

### Context-first attribution

Use context helpers when you want workflow or feature attribution to apply across nested calls without threading more parameters through your own service signatures.

- `WithAttribution(...)`
- `RunWithAttribution(...)`
- `WithWorkflow(...)`
- `RunWithWorkflow(...)`
- `WithTask(...)`
- `RunWithTask(...)`

```go
package main

import (
	"context"

	llmobservability "github.com/cloptima/llm-observability-go"
)

ctx := llmobservability.WithWorkflow(context.Background(), "support_agent", llmobservability.Attribution{
	TenantID: "acme-prod",
})
ctx = llmobservability.WithTask(ctx, "draft_reply", llmobservability.Attribution{
	TeamID: "customer-support",
})

_, _ = llmobservability.ObserveCall(ctx, client, llmobservability.ObserveCallOptions[map[string]any]{
	Provider:     "openai",
	Model:        "gpt-4.1-mini",
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
	ExtractUsage: llmobservability.ExtractOpenAIUsage,
})
```

Per-call attribution still works and overrides context when needed.

For HTTP services, Go also exposes request-context helpers such as `RequestContextMiddleware(...)`, `InstrumentHTTPRequestContext(...)`, and `WithRequestContextData(...)`.

### Shared transport integration

If your application centralizes outbound LLM calls behind `net/http`, instrument that shared boundary:

```go
import (
	"net/http"

	llmobservability "github.com/cloptima/llm-observability-go"
)

httpClient := llmobservability.InstrumentHTTPClient(&http.Client{}, client, llmobservability.TransportInstrumentationOptions{
	RequestOptions: llmobservability.TransportRequestOptions{
		Provider:      "openai",
		Model:         "gpt-4o-mini",
		FireAndForget: llmobservability.Bool(false),
	},
})
```

This gives broad coverage, but it has less business context than call-site or wrapper-boundary integration.

### OTLP delivery to Cloptima

Use `otlp_http` when your enterprise prefers OpenTelemetry-compatible payloads but still wants to send that telemetry to Cloptima.

- `cloptima_http` is the default delivery mode
- `otlp_http` sends OpenTelemetry-compatible payloads to Cloptima's OTLP receiver

```bash
CLOPTIMA_LLM_OBSERVABILITY_DELIVERY_MODE=otlp_http
CLOPTIMA_LLM_OBSERVABILITY_OTLP_SERVICE_NAME=agent-api
CLOPTIMA_LLM_OBSERVABILITY_OTLP_SERVICE_VERSION=2026.06.14
```

If you already operate an OTEL collector and emit GenAI spans, you can also send OTLP data to Cloptima without using this SDK. Use the SDK OTLP mode when you want application-managed instrumentation that still fits an OTLP-shaped delivery contract.

## Built-in extractors and compatibility

Built-in usage extractors cover:

- OpenAI
- Azure OpenAI
- Anthropic
- Gemini
- Vertex AI
- Bedrock

If a provider reports image, audio, or video token usage, the built-in extractors capture those units in fields such as `input_image`, `output_image`, `input_audio`, and `output_video`. When Cloptima has pricing for that model, those units can be included in cost reporting.

If a provider returns a direct charge, pass or preserve it as `VendorReportedCostUSD`.

The SDK does not invent media charges for providers that bill by image count, video duration, resolution, or other non-token measures when the provider response does not expose enough pricing data. In those cases, either:

- preserve the provider-reported cost when available
- map the provider's usage fields into `ExtraUsageUnits`
- or add your own custom extractor until the provider exposes a stable shape

If a provider response shape drifts, you do not need to replace the whole extractor path. Compose or patch it instead:

- `TryExtractUsage(...)`
- `ComposeUsageExtractors(...)`
- `WithUsageOverrides(...)`
- `CreateMappedUsageExtractor(...)`
- `ListSupportedProviders()`

Example:

```go
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
```

## Attribution fields

Common ownership and reporting fields:

- `AppID`
- `Environment`
- `TeamID`
- `FeatureID`
- `WorkflowID`
- `CostCenter`
- `BusinessUnit`
- `Product`
- `TenantID`
- `EndCustomerID`
- `CustomerSegment`
- `Release`

Set defaults once in `DefaultAttribution`, set them in context, or override them per call.

## Metadata and privacy

Use `MetadataPrivacyOptions` to control how custom metadata is retained:

- `metadata_only`
- `allowlisted_metadata`
- `strict_finops`
- `debug_observability`

Sensitive-looking keys such as prompts, messages, credentials, and secrets are treated conservatively by default.

If you need the built-in finance-safe custom metadata allowlist for `strict_finops`, use `StrictFinopsMetadataKeys()`.

## Validation and local previews

Use these helpers in local tests, CI, or rollout checks:

- `PreviewEventPayload(...)`
- `PreviewBatchPayload(...)`
- `PreviewOTLPRequest(...)`
- `ValidatePayload(...)`

They build or validate payloads in memory and do not send network traffic.

## Examples

Public examples live in `examples/`:

- `basic/`: direct call-site integration
- `custom-wrapper/`: existing service wrapper integration using Go wrapper factories
- `workflow-context/`: context-first attribution without signature bloat
- `http-transport/`: shared `net/http` integration
- `multimodal-tokens/`: token-based multimodal usage extraction for image, audio, and video inputs and outputs
- `mapped-extractor/`: adapt a provider or internal wrapper response without rewriting your integration
- `otlp-basic/`: OTLP-compatible delivery to Cloptima
- `openai-basic/`, `anthropic-basic/`, `gemini-basic/`: provider-specific extractor examples

## Troubleshooting

No telemetry arrives:

- verify the API key is valid for Cloptima telemetry ingestion
- check `client.IsEnabled()`
- inspect a sample event with `ValidatePayload(PreviewEventPayload(...))`

Unexpected provider response shape:

- start with the closest built-in extractor
- patch field differences with `WithUsageOverrides(...)` or `CreateMappedUsageExtractor(...)`
- compare against `ListSupportedProviders()` if you need a supported-provider snapshot

## Support

- Issues: `https://github.com/cloptima/llm-observability-go/issues`
- Security: see `SECURITY.md`
- Product support: `hello@cloptima.ai`
