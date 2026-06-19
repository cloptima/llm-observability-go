package llmobservability

import (
	"context"
	"errors"
	"time"
)

type ObserveStreamOptions[T any] struct {
	Provider          string
	Model             string
	Call              func(func(T) error) error
	ExtractUsage      func([]T) ExtractedUsage
	Attribution       Attribution
	Agent             AgentContext
	Metadata          map[string]any
	MetadataPrivacy   *MetadataPrivacyOptions
	RequestID         string
	TraceID           string
	FireAndForget     *bool
	MaxBufferedChunks int
}

func ObserveStream[T any](ctx context.Context, client Client, options ObserveStreamOptions[T], onChunk func(T) error) error {
	if options.Call == nil {
		return errors.New("call is required")
	}
	if onChunk == nil {
		return errors.New("onChunk is required")
	}
	maxBuffered := options.MaxBufferedChunks
	if maxBuffered <= 0 {
		maxBuffered = 256
	}
	buffer := make([]T, 0, maxBuffered)
	emittedChunks := 0
	started := time.Now().UTC()
	startedMonotonic := time.Now()
	emit := func(chunk T) error {
		emittedChunks++
		if options.ExtractUsage != nil {
			if len(buffer) == maxBuffered {
				copy(buffer, buffer[1:])
				buffer[len(buffer)-1] = chunk
			} else {
				buffer = append(buffer, chunk)
			}
		}
		return onChunk(chunk)
	}

	err := options.Call(emit)
	completed := time.Now().UTC()
	latency := int(time.Since(startedMonotonic).Milliseconds())
	if latency < 0 {
		latency = 0
	}
	event := UsageEvent{
		Provider:    options.Provider,
		Model:       options.Model,
		RequestID:   options.RequestID,
		TraceID:     options.TraceID,
		Status:      "succeeded",
		StartedAt:   &started,
		CompletedAt: &completed,
		LatencyMS:   Int(latency),
		Attribution: options.Attribution,
		Agent:       options.Agent,
		Metadata:    cloneAnyMap(options.Metadata),
	}
	if event.Metadata == nil {
		event.Metadata = map[string]any{}
	}
	event.Metadata["streamed"] = true
	if options.ExtractUsage != nil && err == nil {
		applyExtractedUsage(&event, options.ExtractUsage(buffer))
	}
	if err != nil {
		if emittedChunks > 0 {
			event.Status = "partial"
		} else {
			event.Status = "failed"
		}
		event.ErrorMessage = err.Error()
		event.Metadata["stream_chunks"] = emittedChunks
	}
	if event.Provider == "" {
		event.Provider = options.Provider
	}
	if event.Model == "" {
		event.Model = options.Model
	}
	if postErr := recordObservedEvent(ctx, client, event, options.MetadataPrivacy, resolveBoolOption(options.FireAndForget, true)); postErr != nil {
		reportObserveError(client, postErr)
	}
	return err
}

func CreateObservedCall[T any](ctx context.Context, client Client, defaults ObserveCallOptions[T]) func(func() (T, error), *ObserveCallOptions[T]) (T, error) {
	return func(call func() (T, error), overrides *ObserveCallOptions[T]) (T, error) {
		options := defaults
		options.Call = call
		if overrides != nil {
			options = mergeObserveCallOptions(options, *overrides)
			options.Call = call
		}
		return ObserveCall(ctx, client, options)
	}
}

func CreateObservedStream[T any](ctx context.Context, client Client, defaults ObserveStreamOptions[T]) func(func(func(T) error) error, func(T) error, *ObserveStreamOptions[T]) error {
	return func(call func(func(T) error) error, onChunk func(T) error, overrides *ObserveStreamOptions[T]) error {
		options := defaults
		options.Call = call
		if overrides != nil {
			options = mergeObserveStreamOptions(options, *overrides)
			options.Call = call
		}
		return ObserveStream(ctx, client, options, onChunk)
	}
}

func BindObservedCall[T any](ctx context.Context, client Client, fn func() (T, error), defaults ObserveCallOptions[T]) func() (T, error) {
	observed := CreateObservedCall(ctx, client, defaults)
	return func() (T, error) {
		return observed(fn, nil)
	}
}

func BindObservedStream[T any](ctx context.Context, client Client, fn func(func(T) error) error, defaults ObserveStreamOptions[T]) func(func(T) error) error {
	observed := CreateObservedStream(ctx, client, defaults)
	return func(onChunk func(T) error) error {
		return observed(fn, onChunk, nil)
	}
}

func mergeObserveCallOptions[T any](base ObserveCallOptions[T], override ObserveCallOptions[T]) ObserveCallOptions[T] {
	if override.Provider != "" {
		base.Provider = override.Provider
	}
	if override.Model != "" {
		base.Model = override.Model
	}
	if override.ExtractUsage != nil {
		base.ExtractUsage = override.ExtractUsage
	}
	base.Attribution = mergeAttribution(base.Attribution, override.Attribution)
	base.Agent = mergeAgentContext(base.Agent, override.Agent)
	base.Metadata = mergeMetadata(base.Metadata, override.Metadata)
	if override.MetadataPrivacy != nil {
		base.MetadataPrivacy = override.MetadataPrivacy
	}
	if override.RequestID != "" {
		base.RequestID = override.RequestID
	}
	if override.TraceID != "" {
		base.TraceID = override.TraceID
	}
	if override.FireAndForget != nil {
		base.FireAndForget = Bool(*override.FireAndForget)
	}
	return base
}

func mergeObserveStreamOptions[T any](base ObserveStreamOptions[T], override ObserveStreamOptions[T]) ObserveStreamOptions[T] {
	if override.Provider != "" {
		base.Provider = override.Provider
	}
	if override.Model != "" {
		base.Model = override.Model
	}
	if override.ExtractUsage != nil {
		base.ExtractUsage = override.ExtractUsage
	}
	base.Attribution = mergeAttribution(base.Attribution, override.Attribution)
	base.Agent = mergeAgentContext(base.Agent, override.Agent)
	base.Metadata = mergeMetadata(base.Metadata, override.Metadata)
	if override.MetadataPrivacy != nil {
		base.MetadataPrivacy = override.MetadataPrivacy
	}
	if override.RequestID != "" {
		base.RequestID = override.RequestID
	}
	if override.TraceID != "" {
		base.TraceID = override.TraceID
	}
	if override.FireAndForget != nil {
		base.FireAndForget = Bool(*override.FireAndForget)
	}
	if override.MaxBufferedChunks > 0 {
		base.MaxBufferedChunks = override.MaxBufferedChunks
	}
	return base
}

func mergeAgentContext(base AgentContext, overlay AgentContext) AgentContext {
	if overlay.AgentSessionID != "" {
		base.AgentSessionID = overlay.AgentSessionID
	}
	if overlay.AgentRunID != "" {
		base.AgentRunID = overlay.AgentRunID
	}
	if overlay.ParentExecutionID != "" {
		base.ParentExecutionID = overlay.ParentExecutionID
	}
	if overlay.AgentStepID != "" {
		base.AgentStepID = overlay.AgentStepID
	}
	if overlay.ToolCallID != "" {
		base.ToolCallID = overlay.ToolCallID
	}
	if overlay.ToolName != "" {
		base.ToolName = overlay.ToolName
	}
	if overlay.RetryIndex != nil {
		base.RetryIndex = Int(*overlay.RetryIndex)
	}
	if overlay.LoopIteration != nil {
		base.LoopIteration = Int(*overlay.LoopIteration)
	}
	return base
}

func mergeMetadata(base map[string]any, overlay map[string]any) map[string]any {
	if len(base) == 0 && len(overlay) == 0 {
		return nil
	}
	result := cloneAnyMap(base)
	for key, value := range overlay {
		result[key] = value
	}
	return result
}
