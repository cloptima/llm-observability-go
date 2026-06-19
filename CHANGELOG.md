# Changelog

## 0.2.2

- Initial public beta release of the Cloptima Go LLM observability SDK.
- Added `InitFromEnv(...)` for environment-based setup and disabled pass-through behavior when the SDK is not configured.
- Added `ObserveCall(...)` and `ObserveStream(...)` for instrumenting application-level LLM calls.
- Added `CreateObservedCall(...)`, `CreateObservedStream(...)`, `BindObservedCall(...)`, and `BindObservedStream(...)` for reusable wrapper-boundary integrations.
- Added `InstrumentRoundTripper(...)`, `InstrumentHTTPClient(...)`, and OpenAI-compatible helpers for shared `net/http` instrumentation.
- Added context-first attribution helpers for workflows, tasks, request metadata, and agent-style execution context.
- Added close and signal-shutdown helpers so short-lived Go services can flush buffered telemetry before exit.
- Added payload preview and validation helpers for local testing and CI checks.
- Added OTLP-compatible delivery to Cloptima and OTLP preview helpers.
- Added provider extractors, mapped extractors, multimodal usage support, and public examples for common onboarding paths.
