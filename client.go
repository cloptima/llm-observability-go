package llmobservability

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	mathrand "math/rand"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	PackageVersion            = "0.2.2"
	SDKEventSchemaVersion     = "cloptima.llm.event.v1"
	SDKBatchSchemaVersion     = "cloptima.llm.batch.v1"
	DefaultAPIBaseURL         = "https://api.cloptima.ai"
	SDKIngestPath             = "/v1/ai/integrations/sdk/events"
	OTLPTracesPath            = "/v1/ai/integrations/otlp/traces"
	InitEnvPrefix             = "CLOPTIMA_LLM_OBSERVABILITY_"
	InitEnabledEnv            = InitEnvPrefix + "ENABLED"
	InitAPIBaseURLEnv         = InitEnvPrefix + "API_BASE_URL"
	InitAPIKeyEnv             = InitEnvPrefix + "API_KEY"
	InitAppIDEnv              = InitEnvPrefix + "APP_ID"
	InitEnvironmentEnv        = InitEnvPrefix + "ENVIRONMENT"
	InitTeamIDEnv             = InitEnvPrefix + "TEAM_ID"
	InitDeliveryModeEnv       = InitEnvPrefix + "DELIVERY_MODE"
	InitOTLPServiceNameEnv    = InitEnvPrefix + "OTLP_SERVICE_NAME"
	InitOTLPServiceVersionEnv = InitEnvPrefix + "OTLP_SERVICE_VERSION"
)

const (
	DeliveryModeCloptimaHTTP DeliveryMode = "cloptima_http"
	DeliveryModeOTLPHTTP     DeliveryMode = "otlp_http"
)

const (
	MetadataModeMetadataOnly       = "metadata_only"
	MetadataModeAllowlisted        = "allowlisted_metadata"
	MetadataModeStrictFinops       = "strict_finops"
	MetadataModeDebugObservability = "debug_observability"
)

type DeliveryMode string

type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

type Client interface {
	IsEnabled() bool
	InitError() error
	Record(ctx context.Context, event UsageEvent) error
	RecordBatch(ctx context.Context, events []UsageEvent) error
	RecordAsync(ctx context.Context, event UsageEvent)
	Stats() Stats
	Flush(timeout time.Duration) bool
	Close(timeout time.Duration) bool
}

type Attribution struct {
	TeamID          string
	AppID           string
	FeatureID       string
	WorkflowID      string
	BusinessUnit    string
	CostCenter      string
	Product         string
	CustomerSegment string
	EndCustomerID   string
	TenantID        string
	Release         string
	Environment     string
	ActorID         string
	ActorType       string
}

type AgentContext struct {
	AgentSessionID    string
	AgentRunID        string
	ParentExecutionID string
	AgentStepID       string
	ToolCallID        string
	ToolName          string
	RetryIndex        *int
	LoopIteration     *int
}

type UsageEvent struct {
	Provider              string
	Model                 string
	SourceEventID         string
	RequestID             string
	ProviderRequestID     string
	TraceID               string
	Status                string
	InputTokens           *int
	OutputTokens          *int
	TotalTokens           *int
	ReasoningTokens       *int
	CachedInputTokens     *int
	ExtraUsageUnits       map[string]int
	CacheHit              *bool
	VendorReportedCostUSD any
	StartedAt             *time.Time
	CompletedAt           *time.Time
	LatencyMS             *int
	ErrorMessage          string
	Attribution           Attribution
	Agent                 AgentContext
	Metadata              map[string]any
}

type MetadataDropInfo struct {
	KeyPath string
	Reason  string
	Mode    string
}

type MetadataPrivacyOptions struct {
	Mode               string
	AllowlistKeys      []string
	DenylistKeys       []string
	RedactKeys         []string
	HashKeys           []string
	MaxKeys            int
	MaxValueLength     int
	MaxSerializedBytes int
	RedactValue        string
	OnMetadataDrop     func(MetadataDropInfo)
}

type Config struct {
	APIBaseURL            string
	APIKey                string
	DefaultAttribution    Attribution
	DeliveryMode          DeliveryMode
	OTLPHeaders           map[string]string
	OTLPServiceName       string
	OTLPServiceVersion    string
	SDKName               string
	SDKVersion            string
	HTTPClient            HTTPDoer
	Timeout               time.Duration
	MetadataPrivacy       *MetadataPrivacyOptions
	OnError               func(error)
	OnDrop                func(UsageEvent, string)
	AsyncQueueMaxSize     int
	AsyncBatchSize        int
	AsyncFlushInterval    time.Duration
	AsyncRetryCount       int
	AsyncRetryBackoff     time.Duration
	AsyncRetryJitterRatio float64
}

type InitOptions struct {
	Env                   map[string]string
	Enabled               *bool
	Strict                bool
	OnInitError           func(error)
	APIBaseURL            string
	APIKey                string
	DefaultAttribution    Attribution
	DeliveryMode          DeliveryMode
	OTLPHeaders           map[string]string
	OTLPServiceName       string
	OTLPServiceVersion    string
	SDKName               string
	SDKVersion            string
	HTTPClient            HTTPDoer
	Timeout               time.Duration
	MetadataPrivacy       *MetadataPrivacyOptions
	OnError               func(error)
	OnDrop                func(UsageEvent, string)
	AsyncQueueMaxSize     int
	AsyncBatchSize        int
	AsyncFlushInterval    time.Duration
	AsyncRetryCount       int
	AsyncRetryBackoff     time.Duration
	AsyncRetryJitterRatio float64
}

type Stats struct {
	QueuedEvents    int64 `json:"queued_events"`
	DroppedEvents   int64 `json:"dropped_events"`
	DeliveredEvents int64 `json:"delivered_events"`
	FailedBatches   int64 `json:"failed_batches"`
}

type ValidationResult struct {
	Valid  bool     `json:"valid"`
	Errors []string `json:"errors"`
}

type ObserveCallOptions[T any] struct {
	Provider        string
	Model           string
	Call            func() (T, error)
	ExtractUsage    func(any) ExtractedUsage
	Attribution     Attribution
	Agent           AgentContext
	Metadata        map[string]any
	MetadataPrivacy *MetadataPrivacyOptions
	RequestID       string
	TraceID         string
	FireAndForget   *bool
}

type PreviewOptions struct {
	DefaultAttribution Attribution
	SDKName            string
	SDKVersion         string
	MetadataPrivacy    *MetadataPrivacyOptions
}

type OTLPPayloadOptions struct {
	SDKName        string
	SDKVersion     string
	ServiceName    string
	ServiceVersion string
}

type queuedEvent struct {
	event           UsageEvent
	metadataPrivacy *MetadataPrivacyOptions
}

type observabilityClient struct {
	apiBaseURL            string
	ingestURL             string
	otlpURL               string
	apiKey                string
	defaultAttribution    Attribution
	deliveryMode          DeliveryMode
	otlpHeaders           map[string]string
	otlpServiceName       string
	otlpServiceVersion    string
	sdkName               string
	sdkVersion            string
	httpClient            HTTPDoer
	timeout               time.Duration
	metadataPrivacy       resolvedMetadataPrivacy
	onError               func(error)
	onDrop                func(UsageEvent, string)
	asyncBatchSize        int
	asyncFlushInterval    time.Duration
	asyncRetryCount       int
	asyncRetryBackoff     time.Duration
	asyncRetryJitterRatio float64
	queue                 chan queuedEvent
	wg                    sync.WaitGroup
	mu                    sync.Mutex
	workerStarted         bool
	closed                bool
	droppedEvents         atomic.Int64
	deliveredEvents       atomic.Int64
	failedBatches         atomic.Int64
}

type disabledClient struct {
	initErr error
}

type attributionContextKey struct{}

type resolvedMetadataPrivacy struct {
	Mode               string
	AllowlistKeys      map[string]struct{}
	DenylistKeys       map[string]struct{}
	RedactKeys         map[string]struct{}
	HashKeys           map[string]struct{}
	MaxKeys            int
	MaxValueLength     int
	MaxSerializedBytes int
	RedactValue        string
	OnMetadataDrop     func(MetadataDropInfo)
}

var (
	defaultSensitiveMetadataKeyPatterns = []string{
		"authorization",
		"api_key",
		"apikey",
		"secret",
		"password",
		"token",
		"cookie",
		"prompt",
		"completion",
		"message",
		"body",
		"content",
		"input",
		"output",
	}
	strictFinopsMetadataKeyList = []string{
		"route",
		"path",
		"method",
		"host",
		"status_code",
		"http_method",
		"http_route",
		"http_path",
		"http_host",
		"request_id",
		"trace_id",
		"provider_region",
		"provider_account",
		"service_name",
		"workspace",
		"tenant_slug",
		"org_slug",
		"customer_tier",
		"deployment",
		"region",
	}
	strictFinopsMetadataKeys = normalizeRuleKeys(strictFinopsMetadataKeyList)
)

func NewClient(cfg Config) (Client, error) {
	apiBaseURL, err := resolveAPIBaseURL(cfg.APIBaseURL)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("api key is required")
	}
	if strings.TrimSpace(cfg.DefaultAttribution.AppID) == "" {
		return nil, errors.New("default attribution app id is required")
	}
	if strings.TrimSpace(cfg.DefaultAttribution.Environment) == "" {
		return nil, errors.New("default attribution environment is required")
	}

	deliveryMode := resolveDeliveryMode(cfg.DeliveryMode)
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout}
	}
	asyncQueueMaxSize := cfg.AsyncQueueMaxSize
	if asyncQueueMaxSize <= 0 {
		asyncQueueMaxSize = 1000
	}
	asyncBatchSize := cfg.AsyncBatchSize
	if asyncBatchSize <= 0 {
		asyncBatchSize = 20
	}
	asyncFlushInterval := cfg.AsyncFlushInterval
	if asyncFlushInterval < 0 {
		asyncFlushInterval = 0
	}
	asyncRetryBackoff := cfg.AsyncRetryBackoff
	if asyncRetryBackoff < 0 {
		asyncRetryBackoff = 0
	}
	asyncRetryJitterRatio := cfg.AsyncRetryJitterRatio
	if asyncRetryJitterRatio < 0 {
		asyncRetryJitterRatio = 0
	}
	if asyncRetryJitterRatio > 1 {
		asyncRetryJitterRatio = 1
	}
	sdkName := strings.TrimSpace(cfg.SDKName)
	if sdkName == "" {
		sdkName = "cloptima-llm-observability-go"
	}
	otlpServiceName := strings.TrimSpace(cfg.OTLPServiceName)
	if otlpServiceName == "" {
		otlpServiceName = sdkName
	}
	return &observabilityClient{
		apiBaseURL:            apiBaseURL,
		ingestURL:             apiBaseURL + SDKIngestPath,
		otlpURL:               apiBaseURL + OTLPTracesPath,
		apiKey:                strings.TrimSpace(cfg.APIKey),
		defaultAttribution:    cfg.DefaultAttribution,
		deliveryMode:          deliveryMode,
		otlpHeaders:           cloneStringMap(cfg.OTLPHeaders),
		otlpServiceName:       otlpServiceName,
		otlpServiceVersion:    strings.TrimSpace(cfg.OTLPServiceVersion),
		sdkName:               sdkName,
		sdkVersion:            strings.TrimSpace(cfg.SDKVersion),
		httpClient:            httpClient,
		timeout:               timeout,
		metadataPrivacy:       resolveMetadataPrivacy(cfg.MetadataPrivacy),
		onError:               cfg.OnError,
		onDrop:                cfg.OnDrop,
		asyncBatchSize:        asyncBatchSize,
		asyncFlushInterval:    asyncFlushInterval,
		asyncRetryCount:       maxInt(cfg.AsyncRetryCount, 0),
		asyncRetryBackoff:     asyncRetryBackoff,
		asyncRetryJitterRatio: asyncRetryJitterRatio,
		queue:                 make(chan queuedEvent, asyncQueueMaxSize),
	}, nil
}

func InitFromEnv(options InitOptions) Client {
	env := currentEnv(options.Env)
	if enabled, ok := resolveEnabledFlag(options.Enabled, env); ok && !enabled {
		return Disabled(nil)
	}

	defaultAttr := options.DefaultAttribution
	if defaultAttr.AppID == "" {
		defaultAttr.AppID = strings.TrimSpace(env[InitAppIDEnv])
	}
	if defaultAttr.Environment == "" {
		defaultAttr.Environment = strings.TrimSpace(env[InitEnvironmentEnv])
	}
	if defaultAttr.Environment == "" {
		defaultAttr.Environment = "production"
	}
	if defaultAttr.TeamID == "" {
		defaultAttr.TeamID = strings.TrimSpace(env[InitTeamIDEnv])
	}

	apiKey := strings.TrimSpace(options.APIKey)
	if apiKey == "" {
		apiKey = strings.TrimSpace(env[InitAPIKeyEnv])
	}
	apiBaseURL := strings.TrimSpace(options.APIBaseURL)
	if apiBaseURL == "" {
		apiBaseURL = strings.TrimSpace(env[InitAPIBaseURLEnv])
	}
	if apiBaseURL == "" {
		apiBaseURL = DefaultAPIBaseURL
	}

	missing := make([]string, 0, 2)
	if apiKey == "" {
		missing = append(missing, InitAPIKeyEnv)
	}
	if defaultAttr.AppID == "" {
		missing = append(missing, InitAppIDEnv)
	}
	if len(missing) > 0 {
		err := fmt.Errorf("Cloptima LLM observability is enabled but missing required configuration: %s", strings.Join(missing, ", "))
		if options.OnInitError != nil {
			options.OnInitError(err)
		}
		if options.Strict || isExplicitlyEnabled(options.Enabled, env) {
			return Disabled(err)
		}
		return Disabled(nil)
	}

	deliveryMode := options.DeliveryMode
	if deliveryMode == "" {
		deliveryMode = DeliveryMode(strings.TrimSpace(env[InitDeliveryModeEnv]))
	}
	otlpServiceName := strings.TrimSpace(options.OTLPServiceName)
	if otlpServiceName == "" {
		otlpServiceName = strings.TrimSpace(env[InitOTLPServiceNameEnv])
	}
	otlpServiceVersion := strings.TrimSpace(options.OTLPServiceVersion)
	if otlpServiceVersion == "" {
		otlpServiceVersion = strings.TrimSpace(env[InitOTLPServiceVersionEnv])
	}

	client, err := NewClient(Config{
		APIBaseURL:            apiBaseURL,
		APIKey:                apiKey,
		DefaultAttribution:    defaultAttr,
		DeliveryMode:          deliveryMode,
		OTLPHeaders:           options.OTLPHeaders,
		OTLPServiceName:       otlpServiceName,
		OTLPServiceVersion:    otlpServiceVersion,
		SDKName:               options.SDKName,
		SDKVersion:            options.SDKVersion,
		HTTPClient:            options.HTTPClient,
		Timeout:               options.Timeout,
		MetadataPrivacy:       options.MetadataPrivacy,
		OnError:               options.OnError,
		OnDrop:                options.OnDrop,
		AsyncQueueMaxSize:     options.AsyncQueueMaxSize,
		AsyncBatchSize:        options.AsyncBatchSize,
		AsyncFlushInterval:    options.AsyncFlushInterval,
		AsyncRetryCount:       options.AsyncRetryCount,
		AsyncRetryBackoff:     options.AsyncRetryBackoff,
		AsyncRetryJitterRatio: options.AsyncRetryJitterRatio,
	})
	if err != nil {
		if options.OnInitError != nil {
			options.OnInitError(err)
		}
		return Disabled(err)
	}
	return client
}

func IsEnabled(options InitOptions) bool {
	env := currentEnv(options.Env)
	if enabled, ok := resolveEnabledFlag(options.Enabled, env); ok && !enabled {
		return false
	}
	defaultAttr := options.DefaultAttribution
	if defaultAttr.AppID == "" {
		defaultAttr.AppID = strings.TrimSpace(env[InitAppIDEnv])
	}
	if defaultAttr.Environment == "" {
		defaultAttr.Environment = strings.TrimSpace(env[InitEnvironmentEnv])
	}
	if defaultAttr.Environment == "" {
		defaultAttr.Environment = "production"
	}
	apiKey := strings.TrimSpace(options.APIKey)
	if apiKey == "" {
		apiKey = strings.TrimSpace(env[InitAPIKeyEnv])
	}
	return apiKey != "" && defaultAttr.AppID != "" && defaultAttr.Environment != ""
}

func Disabled(initErr error) Client {
	return &disabledClient{initErr: initErr}
}

func WithAttribution(ctx context.Context, attribution Attribution) context.Context {
	merged := mergeAttribution(currentAttribution(ctx), attribution)
	return context.WithValue(ctx, attributionContextKey{}, merged)
}

func WithWorkflow(ctx context.Context, name string, attribution Attribution) context.Context {
	if strings.TrimSpace(name) != "" && strings.TrimSpace(attribution.WorkflowID) == "" {
		attribution.WorkflowID = strings.TrimSpace(name)
	}
	return WithAttribution(ctx, attribution)
}

func WithTask(ctx context.Context, name string, attribution Attribution) context.Context {
	if strings.TrimSpace(name) != "" && strings.TrimSpace(attribution.FeatureID) == "" {
		attribution.FeatureID = strings.TrimSpace(name)
	}
	return WithAttribution(ctx, attribution)
}

func RunWithAttribution[T any](ctx context.Context, attribution Attribution, fn func(context.Context) T) T {
	return fn(WithAttribution(ctx, attribution))
}

func RunWithWorkflow[T any](ctx context.Context, name string, attribution Attribution, fn func(context.Context) T) T {
	return fn(WithWorkflow(ctx, name, attribution))
}

func RunWithTask[T any](ctx context.Context, name string, attribution Attribution, fn func(context.Context) T) T {
	return fn(WithTask(ctx, name, attribution))
}

func ObserveCall[T any](ctx context.Context, client Client, options ObserveCallOptions[T]) (T, error) {
	var zero T
	if options.Call == nil {
		return zero, errors.New("call is required")
	}
	started := time.Now().UTC()
	startedMonotonic := time.Now()
	result, err := options.Call()
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
	if options.ExtractUsage != nil && err == nil {
		extracted := options.ExtractUsage(result)
		applyExtractedUsage(&event, extracted)
	}
	if err != nil {
		event.Status = "failed"
		event.ErrorMessage = err.Error()
	} else if event.Provider == "" {
		event.Provider = options.Provider
	}
	if event.Model == "" {
		event.Model = options.Model
	}
	postErr := recordObservedEvent(ctx, client, event, options.MetadataPrivacy, resolveBoolOption(options.FireAndForget, true))
	if postErr != nil {
		reportObserveError(client, postErr)
	}
	return result, err
}

func PreviewEventPayload(event UsageEvent, options PreviewOptions) (map[string]any, error) {
	defaults := options.DefaultAttribution
	if strings.TrimSpace(defaults.AppID) == "" {
		defaults.AppID = "preview-app"
	}
	if strings.TrimSpace(defaults.Environment) == "" {
		defaults.Environment = "preview"
	}
	sdkName := strings.TrimSpace(options.SDKName)
	if sdkName == "" {
		sdkName = "cloptima-llm-observability-go"
	}
	payload, err := buildEventPayload(context.Background(), event, defaults, sdkName, strings.TrimSpace(options.SDKVersion), options.MetadataPrivacy)
	if err != nil {
		return nil, err
	}
	return payload, nil
}

func PreviewBatchPayload(events []UsageEvent, options PreviewOptions) (map[string]any, error) {
	if len(events) == 0 {
		return nil, nil
	}
	payloads := make([]map[string]any, 0, len(events))
	for _, event := range events {
		payload, err := PreviewEventPayload(event, options)
		if err != nil {
			return nil, err
		}
		payloads = append(payloads, payload)
	}
	if len(payloads) == 1 {
		return payloads[0], nil
	}
	return map[string]any{
		"schema_version": SDKBatchSchemaVersion,
		"events":         payloads,
	}, nil
}

func PreviewOTLPRequest(payload map[string]any, options OTLPPayloadOptions) map[string]any {
	sdkName := strings.TrimSpace(options.SDKName)
	if sdkName == "" {
		sdkName = "cloptima-llm-observability-go"
	}
	serviceName := strings.TrimSpace(options.ServiceName)
	if serviceName == "" {
		if metadata, ok := payload["metadata"].(map[string]any); ok {
			serviceName = cleanString(metadata["app_id"], "")
		}
	}
	if serviceName == "" {
		serviceName = sdkName
	}
	return payloadToOTLPRequest(payload, sdkName, strings.TrimSpace(options.SDKVersion), serviceName, strings.TrimSpace(options.ServiceVersion))
}

func ValidatePayload(payload map[string]any) ValidationResult {
	errors := validatePayloadMap(payload)
	return ValidationResult{
		Valid:  len(errors) == 0,
		Errors: errors,
	}
}

func Int(value int) *int {
	return &value
}

func Bool(value bool) *bool {
	return &value
}

func StrictFinopsMetadataKeys() []string {
	return append([]string(nil), strictFinopsMetadataKeyList...)
}

func resolveBoolOption(value *bool, defaultValue bool) bool {
	if value == nil {
		return defaultValue
	}
	return *value
}

func (c *observabilityClient) IsEnabled() bool {
	return true
}

func (c *observabilityClient) InitError() error {
	return nil
}

func (c *observabilityClient) Record(ctx context.Context, event UsageEvent) error {
	return c.recordWithPolicy(ctx, event, metadataPrivacyOptionsFromResolved(c.metadataPrivacy))
}

func (c *observabilityClient) recordWithPolicy(ctx context.Context, event UsageEvent, privacy *MetadataPrivacyOptions) error {
	if privacy == nil {
		privacy = metadataPrivacyOptionsFromResolved(c.metadataPrivacy)
	}
	payload, err := buildEventPayload(ctx, event, c.defaultAttribution, c.sdkName, c.resolvedSDKVersion(), privacy)
	if err != nil {
		return err
	}
	return c.postPayload(c.decoratePayload(payload))
}

func (c *observabilityClient) RecordBatch(ctx context.Context, events []UsageEvent) error {
	payload, err := c.batchPayload(ctx, events, nil)
	if err != nil || payload == nil {
		return err
	}
	return c.postPayload(c.decoratePayload(payload))
}

func (c *observabilityClient) RecordAsync(ctx context.Context, event UsageEvent) {
	c.recordAsyncWithPolicy(ctx, event, nil)
}

func (c *observabilityClient) recordAsyncWithPolicy(ctx context.Context, event UsageEvent, privacy *MetadataPrivacyOptions) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		c.recordDrop(event, "client_closed")
		if c.onError != nil {
			c.onError(errors.New("Cloptima LLM observability client is closed"))
		}
		return
	}
	if !c.workerStarted {
		c.workerStarted = true
		go c.worker()
	}
	c.wg.Add(1)
	select {
	case c.queue <- queuedEvent{event: withResolvedContextEvent(ctx, event), metadataPrivacy: privacy}:
		c.mu.Unlock()
	default:
		c.wg.Done()
		c.mu.Unlock()
		c.recordDrop(event, "queue_full")
		if c.onError != nil {
			c.onError(errors.New("Cloptima LLM observability async queue is full"))
		}
	}
}

func (c *observabilityClient) Stats() Stats {
	return Stats{
		QueuedEvents:    int64(len(c.queue)),
		DroppedEvents:   c.droppedEvents.Load(),
		DeliveredEvents: c.deliveredEvents.Load(),
		FailedBatches:   c.failedBatches.Load(),
	}
}

func (c *observabilityClient) Flush(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()
	if timeout <= 0 {
		<-done
		return true
	}
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

func (c *observabilityClient) Close(timeout time.Duration) bool {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return true
	}
	c.closed = true
	close(c.queue)
	c.mu.Unlock()
	return c.Flush(timeout)
}

func (c *observabilityClient) resolvedSDKVersion() string {
	if strings.TrimSpace(c.sdkVersion) != "" {
		return strings.TrimSpace(c.sdkVersion)
	}
	return PackageVersion
}

func (c *observabilityClient) batchPayload(ctx context.Context, events []UsageEvent, privacyOverrides []*MetadataPrivacyOptions) (map[string]any, error) {
	if len(events) == 0 {
		return nil, nil
	}
	payloads := make([]map[string]any, 0, len(events))
	for index, event := range events {
		var privacy *MetadataPrivacyOptions
		if len(privacyOverrides) > index {
			privacy = privacyOverrides[index]
		}
		payload, err := buildEventPayload(ctx, event, c.defaultAttribution, c.sdkName, c.resolvedSDKVersion(), privacy)
		if err != nil {
			return nil, err
		}
		payloads = append(payloads, payload)
	}
	if len(payloads) == 1 {
		return payloads[0], nil
	}
	return map[string]any{
		"schema_version": SDKBatchSchemaVersion,
		"events":         payloads,
	}, nil
}

func (c *observabilityClient) decoratePayload(payload map[string]any) map[string]any {
	decorated := cloneAnyMap(payload)
	decorated["sdk_delivery_stats"] = deliveryStatsPayload(c.Stats())
	if _, ok := payload["events"].([]any); ok {
		decorated["batch_schema_version"] = SDKBatchSchemaVersion
	}
	if _, ok := payload["events"].([]map[string]any); ok {
		decorated["batch_schema_version"] = SDKBatchSchemaVersion
	}
	return decorated
}

func (c *observabilityClient) postPayload(payload map[string]any) error {
	if c.deliveryMode == DeliveryModeOTLPHTTP {
		otlpPayload := payloadToOTLPRequest(payload, c.sdkName, c.resolvedSDKVersion(), c.otlpServiceName, c.otlpServiceVersion)
		return c.postWithRetries(c.otlpURL, otlpPayload, c.otlpHeadersMap(), "Cloptima OTLP ingest")
	}
	return c.postWithRetries(c.ingestURL, payload, c.cloptimaHeaders(), "Cloptima LLM ingest")
}

func (c *observabilityClient) postWithRetries(targetURL string, payload map[string]any, headers map[string]string, label string) error {
	attempts := c.asyncRetryCount + 1
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if err := c.postJSONOnce(targetURL, payload, headers, label); err != nil {
			lastErr = err
			if attempt == attempts-1 {
				return err
			}
			time.Sleep(c.retryDelay(attempt))
			continue
		}
		return nil
	}
	return lastErr
}

func (c *observabilityClient) retryDelay(attempt int) time.Duration {
	if c.asyncRetryBackoff <= 0 {
		return 0
	}
	delay := time.Duration(float64(c.asyncRetryBackoff) * math.Pow(2, float64(attempt)))
	if c.asyncRetryJitterRatio <= 0 {
		return delay
	}
	jitter := time.Duration(float64(delay) * c.asyncRetryJitterRatio * mathrand.Float64())
	return delay + jitter
}

func (c *observabilityClient) postJSONOnce(targetURL string, payload map[string]any, headers map[string]string, label string) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, targetURL, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= http.StatusBadRequest {
		return fmt.Errorf("%s failed with HTTP %d", label, resp.StatusCode)
	}
	return nil
}

func (c *observabilityClient) cloptimaHeaders() map[string]string {
	return map[string]string{
		"content-type":  "application/json",
		"authorization": "Bearer " + c.apiKey,
		"user-agent":    c.sdkName + "/" + c.resolvedSDKVersion(),
	}
}

func (c *observabilityClient) otlpHeadersMap() map[string]string {
	headers := map[string]string{
		"content-type": "application/json",
	}
	for key, value := range c.otlpHeaders {
		headers[key] = value
	}
	if !hasHeader(headers, "authorization") && shouldAttachDefaultOTLPAuthorization(c.otlpURL) {
		headers["authorization"] = "Bearer " + c.apiKey
	}
	if !hasHeader(headers, "user-agent") {
		headers["user-agent"] = c.sdkName + "/" + c.resolvedSDKVersion()
	}
	return headers
}

func (c *observabilityClient) worker() {
	for item := range c.queue {
		batch := []queuedEvent{item}
		timer := time.NewTimer(c.asyncFlushInterval)
	collectLoop:
		for len(batch) < c.asyncBatchSize {
			if c.asyncFlushInterval == 0 {
				select {
				case nextItem, ok := <-c.queue:
					if !ok {
						break collectLoop
					}
					batch = append(batch, nextItem)
				default:
					break collectLoop
				}
				continue
			}
			select {
			case nextItem, ok := <-c.queue:
				if !ok {
					break collectLoop
				}
				batch = append(batch, nextItem)
			case <-timer.C:
				break collectLoop
			}
		}
		if !timer.Stop() && c.asyncFlushInterval > 0 {
			select {
			case <-timer.C:
			default:
			}
		}
		if err := c.recordAsyncBatch(batch); err != nil {
			c.failedBatches.Add(1)
			if c.onError != nil {
				c.onError(err)
			}
		} else {
			c.deliveredEvents.Add(int64(len(batch)))
		}
		for range batch {
			c.wg.Done()
		}
	}
}

func (c *observabilityClient) recordAsyncBatch(batch []queuedEvent) error {
	if len(batch) == 0 {
		return nil
	}
	events := make([]UsageEvent, 0, len(batch))
	privacies := make([]*MetadataPrivacyOptions, 0, len(batch))
	for _, item := range batch {
		events = append(events, item.event)
		privacies = append(privacies, item.metadataPrivacy)
	}
	payload, err := c.batchPayload(context.Background(), events, privacies)
	if err != nil || payload == nil {
		return err
	}
	return c.postPayload(c.decoratePayload(payload))
}

func (c *observabilityClient) recordDrop(event UsageEvent, reason string) {
	c.droppedEvents.Add(1)
	if c.onDrop != nil {
		c.onDrop(event, reason)
	}
}

func (d *disabledClient) IsEnabled() bool {
	return false
}

func (d *disabledClient) InitError() error {
	return d.initErr
}

func (d *disabledClient) Record(_ context.Context, _ UsageEvent) error {
	return nil
}

func (d *disabledClient) recordWithPolicy(_ context.Context, _ UsageEvent, _ *MetadataPrivacyOptions) error {
	return nil
}

func (d *disabledClient) RecordBatch(_ context.Context, _ []UsageEvent) error {
	return nil
}

func (d *disabledClient) RecordAsync(_ context.Context, _ UsageEvent) {
}

func (d *disabledClient) recordAsyncWithPolicy(_ context.Context, _ UsageEvent, _ *MetadataPrivacyOptions) {
}

func (d *disabledClient) Stats() Stats {
	return Stats{}
}

func (d *disabledClient) Flush(_ time.Duration) bool {
	return true
}

func (d *disabledClient) Close(_ time.Duration) bool {
	return true
}

func buildEventPayload(
	ctx context.Context,
	event UsageEvent,
	defaults Attribution,
	sdkName string,
	sdkVersion string,
	privacyOverride *MetadataPrivacyOptions,
) (map[string]any, error) {
	event = withResolvedContextEvent(ctx, event)
	provider := strings.TrimSpace(event.Provider)
	model := strings.TrimSpace(event.Model)
	if provider == "" || model == "" {
		return nil, errors.New("provider and model are required")
	}
	inputTokens := cleanNonNegativeIntPtr(event.InputTokens)
	outputTokens := cleanNonNegativeIntPtr(event.OutputTokens)
	totalTokens := cleanNonNegativeIntPtr(event.TotalTokens)
	if totalTokens == nil && (inputTokens != nil || outputTokens != nil) {
		totalTokens = Int(valueOrZero(inputTokens) + valueOrZero(outputTokens))
	}
	privacy := resolveMetadataPrivacy(privacyOverride)
	sanitized, err := sanitizeCustomMetadata(cloneAnyMap(event.Metadata), privacy, "")
	if err != nil {
		return nil, err
	}
	mergedAttribution := mergeAttribution(mergeAttribution(defaults, currentAttribution(ctx)), event.Attribution)
	for key, value := range attributionMetadata(mergedAttribution) {
		sanitized[key] = value
	}
	for key, value := range agentMetadata(event.Agent) {
		sanitized[key] = value
	}
	payload := map[string]any{
		"schema_version":           SDKEventSchemaVersion,
		"sdk_name":                 sdkName,
		"sdk_version":              emptyToNil(strings.TrimSpace(sdkVersion)),
		"source_event_id":          resolveSourceEventID(event),
		"request_id":               emptyToNil(strings.TrimSpace(event.RequestID)),
		"provider_request_id":      emptyToNil(strings.TrimSpace(event.ProviderRequestID)),
		"trace_id":                 emptyToNil(strings.TrimSpace(event.TraceID)),
		"provider":                 provider,
		"model":                    model,
		"status":                   statusOrDefault(event.Status),
		"input_tokens":             intPtrToAny(inputTokens),
		"output_tokens":            intPtrToAny(outputTokens),
		"total_tokens":             intPtrToAny(totalTokens),
		"reasoning_tokens":         intPtrToAny(cleanNonNegativeIntPtr(event.ReasoningTokens)),
		"cached_input_tokens":      intPtrToAny(cleanNonNegativeIntPtr(event.CachedInputTokens)),
		"extra_usage_units":        emptyMapToNil(cleanUsageMap(event.ExtraUsageUnits)),
		"cache_hit":                boolPtrToAny(event.CacheHit),
		"vendor_reported_cost_usd": cleanCostValue(event.VendorReportedCostUSD),
		"started_at":               timePtrToAny(event.StartedAt),
		"completed_at":             timePtrToAny(event.CompletedAt),
		"latency_ms":               intPtrToAny(cleanNonNegativeIntPtr(event.LatencyMS)),
		"error_message":            emptyToNil(strings.TrimSpace(event.ErrorMessage)),
		"metadata":                 sanitized,
	}
	return stripNilValues(payload), nil
}

func payloadToOTLPRequest(payload map[string]any, sdkName string, sdkVersion string, serviceName string, serviceVersion string) map[string]any {
	events := extractPayloadEvents(payload)
	now := time.Now().UTC()
	spans := make([]map[string]any, 0, len(events))
	for _, event := range events {
		latency := cleanNonNegativeIntValue(event["latency_ms"])
		startTime := unixNanoString(event["started_at"], now)
		endTime := unixNanoString(event["completed_at"], now.Add(time.Duration(latency)*time.Millisecond))
		spans = append(spans, map[string]any{
			"traceId":           randomHex(16),
			"spanId":            randomHex(8),
			"name":              fmt.Sprintf("llm.%s.%s", cleanString(event["provider"], "unknown"), cleanString(event["model"], "unknown")),
			"kind":              3,
			"startTimeUnixNano": startTime,
			"endTimeUnixNano":   endTime,
			"attributes":        otlpAttributesFromPayload(event),
			"status": stripNilValues(map[string]any{
				"code":    otlpStatusCode(event["status"]),
				"message": emptyToNil(strings.TrimSpace(asString(event["error_message"]))),
			}),
		})
	}
	resourceAttrs := []map[string]any{
		{"key": "service.name", "value": map[string]any{"stringValue": serviceName}},
	}
	if strings.TrimSpace(serviceVersion) != "" {
		resourceAttrs = append(resourceAttrs, map[string]any{"key": "service.version", "value": map[string]any{"stringValue": strings.TrimSpace(serviceVersion)}})
	}
	scope := map[string]any{"name": sdkName}
	if strings.TrimSpace(sdkVersion) != "" {
		scope["version"] = strings.TrimSpace(sdkVersion)
	}
	return map[string]any{
		"resourceSpans": []map[string]any{
			{
				"resource": map[string]any{
					"attributes": resourceAttrs,
				},
				"scopeSpans": []map[string]any{
					{
						"scope": scope,
						"spans": spans,
					},
				},
			},
		},
	}
}

func validatePayloadMap(payload map[string]any) []string {
	if eventsRaw, ok := payload["events"]; ok {
		errors := make([]string, 0)
		if asString(payload["schema_version"]) != SDKBatchSchemaVersion {
			errors = append(errors, "batch.schema_version must equal "+SDKBatchSchemaVersion)
		}
		events, ok := eventsRaw.([]any)
		if !ok {
			if typed, okTyped := eventsRaw.([]map[string]any); okTyped {
				for index, event := range typed {
					errors = append(errors, validateSinglePayload(event, fmt.Sprintf("batch.events[%d]", index))...)
				}
				return errors
			}
			errors = append(errors, "batch.events must be an array")
			return errors
		}
		for index, item := range events {
			event, ok := item.(map[string]any)
			if !ok {
				errors = append(errors, fmt.Sprintf("batch.events[%d] must be an object", index))
				continue
			}
			errors = append(errors, validateSinglePayload(event, fmt.Sprintf("batch.events[%d]", index))...)
		}
		return errors
	}
	return validateSinglePayload(payload, "event")
}

func validateSinglePayload(payload map[string]any, prefix string) []string {
	errors := make([]string, 0)
	if asString(payload["schema_version"]) != SDKEventSchemaVersion {
		errors = append(errors, prefix+".schema_version must equal "+SDKEventSchemaVersion)
	}
	if strings.TrimSpace(asString(payload["sdk_name"])) == "" {
		errors = append(errors, prefix+".sdk_name is required")
	}
	if strings.TrimSpace(asString(payload["source_event_id"])) == "" {
		errors = append(errors, prefix+".source_event_id is required")
	}
	if strings.TrimSpace(asString(payload["provider"])) == "" {
		errors = append(errors, prefix+".provider is required")
	}
	if strings.TrimSpace(asString(payload["model"])) == "" {
		errors = append(errors, prefix+".model is required")
	}
	if status := strings.TrimSpace(asString(payload["status"])); status != "" && status != "succeeded" && status != "failed" && status != "partial" && status != "blocked" {
		errors = append(errors, prefix+".status must be one of succeeded, failed, partial, blocked")
	}
	metadata, ok := payload["metadata"].(map[string]any)
	if !ok {
		errors = append(errors, prefix+".metadata must be an object")
		return errors
	}
	if strings.TrimSpace(asString(metadata["app_id"])) == "" {
		errors = append(errors, prefix+".metadata.app_id is required")
	}
	if strings.TrimSpace(asString(metadata["environment"])) == "" {
		errors = append(errors, prefix+".metadata.environment is required")
	}
	return errors
}

func currentEnv(env map[string]string) map[string]string {
	if env != nil {
		return env
	}
	values := make(map[string]string)
	for _, pair := range os.Environ() {
		key, value, found := strings.Cut(pair, "=")
		if found {
			values[key] = value
		}
	}
	return values
}

func resolveEnabledFlag(explicit *bool, env map[string]string) (bool, bool) {
	if explicit != nil {
		return *explicit, true
	}
	return parseBool(env[InitEnabledEnv])
}

func isExplicitlyEnabled(explicit *bool, env map[string]string) bool {
	if explicit != nil {
		return *explicit
	}
	value, ok := parseBool(env[InitEnabledEnv])
	return ok && value
}

func parseBool(value string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	default:
		return false, false
	}
}

func resolveDeliveryMode(mode DeliveryMode) DeliveryMode {
	switch mode {
	case DeliveryModeOTLPHTTP:
		return DeliveryModeOTLPHTTP
	case "":
		return DeliveryModeCloptimaHTTP
	default:
		return DeliveryModeCloptimaHTTP
	}
}

func resolveAPIBaseURL(value string) (string, error) {
	candidate := withDefaultAPIBaseScheme(strings.TrimSpace(value))
	if candidate == "" {
		candidate = DefaultAPIBaseURL
	}
	parsed, err := url.Parse(candidate)
	if err != nil {
		return "", fmt.Errorf("invalid Cloptima API base URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("invalid Cloptima API base URL: %s", value)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("invalid Cloptima API base URL: %s", value)
	}
	if parsed.Path != "" && parsed.Path != "/" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("Cloptima API base URL must not include a path, query, or hash: %s", value)
	}
	return strings.TrimRight(parsed.Scheme+"://"+parsed.Host, "/"), nil
}

func withDefaultAPIBaseScheme(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return DefaultAPIBaseURL
	}
	if strings.Contains(trimmed, "://") {
		return trimmed
	}
	if strings.HasPrefix(trimmed, "//") {
		return "https:" + trimmed
	}
	if isLocalAPIBaseURL(trimmed) {
		return "http://" + trimmed
	}
	return "https://" + trimmed
}

func isLocalAPIBaseURL(value string) bool {
	trimmed := strings.TrimSpace(value)
	return strings.HasPrefix(trimmed, "localhost") ||
		strings.HasPrefix(trimmed, "127.") ||
		strings.HasPrefix(trimmed, "0.0.0.0") ||
		strings.HasPrefix(trimmed, "[::1]")
}

func currentAttribution(ctx context.Context) Attribution {
	if ctx == nil {
		return Attribution{}
	}
	if value, ok := ctx.Value(attributionContextKey{}).(Attribution); ok {
		return value
	}
	return Attribution{}
}

func withResolvedContextEvent(ctx context.Context, event UsageEvent) UsageEvent {
	requestContext := CurrentRequestContextData(ctx)
	event.Attribution = mergeAttribution(mergeAttribution(currentAttribution(ctx), requestContext.Attribution), event.Attribution)
	if strings.TrimSpace(event.RequestID) == "" {
		event.RequestID = requestContext.RequestID
	}
	if strings.TrimSpace(event.TraceID) == "" {
		event.TraceID = requestContext.TraceID
	}
	event.Metadata = mergeMetadata(requestContext.Metadata, event.Metadata)
	return event
}

func mergeAttribution(base Attribution, overlay Attribution) Attribution {
	merged := base
	if overlay.TeamID != "" {
		merged.TeamID = overlay.TeamID
	}
	if overlay.AppID != "" {
		merged.AppID = overlay.AppID
	}
	if overlay.FeatureID != "" {
		merged.FeatureID = overlay.FeatureID
	}
	if overlay.WorkflowID != "" {
		merged.WorkflowID = overlay.WorkflowID
	}
	if overlay.BusinessUnit != "" {
		merged.BusinessUnit = overlay.BusinessUnit
	}
	if overlay.CostCenter != "" {
		merged.CostCenter = overlay.CostCenter
	}
	if overlay.Product != "" {
		merged.Product = overlay.Product
	}
	if overlay.CustomerSegment != "" {
		merged.CustomerSegment = overlay.CustomerSegment
	}
	if overlay.EndCustomerID != "" {
		merged.EndCustomerID = overlay.EndCustomerID
	}
	if overlay.TenantID != "" {
		merged.TenantID = overlay.TenantID
	}
	if overlay.Release != "" {
		merged.Release = overlay.Release
	}
	if overlay.Environment != "" {
		merged.Environment = overlay.Environment
	}
	if overlay.ActorID != "" {
		merged.ActorID = overlay.ActorID
	}
	if overlay.ActorType != "" {
		merged.ActorType = overlay.ActorType
	}
	return merged
}

func attributionMetadata(attribution Attribution) map[string]any {
	return stripNilValues(map[string]any{
		"team_id":          emptyToNil(attribution.TeamID),
		"app_id":           emptyToNil(attribution.AppID),
		"feature_id":       emptyToNil(attribution.FeatureID),
		"workflow_id":      emptyToNil(attribution.WorkflowID),
		"business_unit":    emptyToNil(attribution.BusinessUnit),
		"cost_center":      emptyToNil(attribution.CostCenter),
		"product":          emptyToNil(attribution.Product),
		"customer_segment": emptyToNil(attribution.CustomerSegment),
		"end_customer_id":  emptyToNil(attribution.EndCustomerID),
		"tenant_id":        emptyToNil(attribution.TenantID),
		"release":          emptyToNil(attribution.Release),
		"environment":      emptyToNil(attribution.Environment),
		"actor_id":         emptyToNil(attribution.ActorID),
		"actor_type":       emptyToNil(attribution.ActorType),
	})
}

func agentMetadata(agent AgentContext) map[string]any {
	return stripNilValues(map[string]any{
		"agent_session_id":    emptyToNil(agent.AgentSessionID),
		"agent_run_id":        emptyToNil(agent.AgentRunID),
		"parent_execution_id": emptyToNil(agent.ParentExecutionID),
		"agent_step_id":       emptyToNil(agent.AgentStepID),
		"tool_call_id":        emptyToNil(agent.ToolCallID),
		"tool_name":           emptyToNil(agent.ToolName),
		"retry_index":         intPtrToAny(cleanNonNegativeIntPtr(agent.RetryIndex)),
		"loop_iteration":      intPtrToAny(cleanNonNegativeIntPtr(agent.LoopIteration)),
	})
}

func resolveSourceEventID(event UsageEvent) string {
	for _, candidate := range []string{
		event.SourceEventID,
		event.RequestID,
		event.ProviderRequestID,
		event.TraceID,
	} {
		if strings.TrimSpace(candidate) != "" {
			return strings.TrimSpace(candidate)
		}
	}
	return fallbackSourceEventID()
}

func fallbackSourceEventID() string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "clop_evt_" + randomHex(16)
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(bytes)
	return "clop_evt_" + encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}

func cleanUsageMap(values map[string]int) map[string]int {
	if len(values) == 0 {
		return nil
	}
	cleaned := make(map[string]int)
	for key, value := range values {
		key = strings.TrimSpace(key)
		if key == "" || value < 0 {
			continue
		}
		cleaned[key] = value
	}
	if len(cleaned) == 0 {
		return nil
	}
	return cleaned
}

func deliveryStatsPayload(stats Stats) map[string]any {
	totalHandled := stats.DroppedEvents + stats.DeliveredEvents
	payload := map[string]any{
		"queued_events":    stats.QueuedEvents,
		"dropped_events":   stats.DroppedEvents,
		"delivered_events": stats.DeliveredEvents,
		"failed_batches":   stats.FailedBatches,
		"recorded_at":      time.Now().UTC().Format(time.RFC3339Nano),
	}
	if totalHandled > 0 {
		payload["drop_rate"] = float64(stats.DroppedEvents) / float64(totalHandled)
	}
	return payload
}

func resolveMetadataPrivacy(options *MetadataPrivacyOptions) resolvedMetadataPrivacy {
	mode := MetadataModeMetadataOnly
	if options != nil && strings.TrimSpace(options.Mode) != "" {
		mode = strings.TrimSpace(options.Mode)
	}
	maxKeys := 64
	maxValueLength := 512
	maxSerializedBytes := 8192
	redactValue := "[redacted]"
	if options != nil {
		if options.MaxKeys > 0 {
			maxKeys = options.MaxKeys
		}
		if options.MaxValueLength > 0 {
			maxValueLength = options.MaxValueLength
		}
		if options.MaxSerializedBytes > 0 {
			maxSerializedBytes = options.MaxSerializedBytes
		}
		if strings.TrimSpace(options.RedactValue) != "" {
			redactValue = options.RedactValue
		}
	}
	return resolvedMetadataPrivacy{
		Mode:               mode,
		AllowlistKeys:      normalizeRuleKeys(optionStrings(options, func(o *MetadataPrivacyOptions) []string { return o.AllowlistKeys })),
		DenylistKeys:       normalizeRuleKeys(optionStrings(options, func(o *MetadataPrivacyOptions) []string { return o.DenylistKeys })),
		RedactKeys:         normalizeRuleKeys(optionStrings(options, func(o *MetadataPrivacyOptions) []string { return o.RedactKeys })),
		HashKeys:           normalizeRuleKeys(optionStrings(options, func(o *MetadataPrivacyOptions) []string { return o.HashKeys })),
		MaxKeys:            maxKeys,
		MaxValueLength:     maxValueLength,
		MaxSerializedBytes: maxSerializedBytes,
		RedactValue:        redactValue,
		OnMetadataDrop:     optionCallback(options),
	}
}

func sanitizeCustomMetadata(metadata map[string]any, privacy resolvedMetadataPrivacy, prefix string) (map[string]any, error) {
	if metadata == nil {
		return map[string]any{}, nil
	}
	result := make(map[string]any)
	accepted := 0
	for rawKey, rawValue := range metadata {
		key := strings.TrimSpace(rawKey)
		if key == "" {
			continue
		}
		keyPath := key
		if prefix != "" {
			keyPath = prefix + "." + key
		}
		if accepted >= privacy.MaxKeys {
			emitMetadataDrop(privacy, keyPath, "max_keys")
			continue
		}
		sanitized, keep, err := sanitizeMetadataValue(rawValue, keyPath, key, privacy)
		if err != nil {
			return nil, err
		}
		if !keep {
			continue
		}
		candidate := cloneAnyMap(result)
		candidate[key] = sanitized
		body, err := json.Marshal(candidate)
		if err != nil {
			return nil, err
		}
		if len(body) > privacy.MaxSerializedBytes {
			emitMetadataDrop(privacy, keyPath, "max_serialized_bytes")
			continue
		}
		result[key] = sanitized
		accepted++
	}
	return result, nil
}

func sanitizeMetadataValue(value any, keyPath string, key string, privacy resolvedMetadataPrivacy) (any, bool, error) {
	if value == nil {
		return nil, false, nil
	}
	if metadataRuleMatches(privacy.DenylistKeys, keyPath, key) {
		emitMetadataDrop(privacy, keyPath, "denylist")
		return nil, false, nil
	}
	if privacy.Mode == MetadataModeAllowlisted && !metadataRuleMatches(privacy.AllowlistKeys, keyPath, key) {
		emitMetadataDrop(privacy, keyPath, "allowlist")
		return nil, false, nil
	}
	if privacy.Mode == MetadataModeStrictFinops {
		if _, ok := strictFinopsMetadataKeys[strings.ToLower(strings.TrimSpace(key))]; !ok {
			emitMetadataDrop(privacy, keyPath, "allowlist")
			return nil, false, nil
		}
	}
	if metadataRuleMatches(privacy.HashKeys, keyPath, key) {
		emitMetadataDrop(privacy, keyPath, "hashed")
		return hashMetadataValue(value), true, nil
	}
	if metadataRuleMatches(privacy.RedactKeys, keyPath, key) || isSensitiveMetadataKey(keyPath, key) {
		emitMetadataDrop(privacy, keyPath, "redacted")
		return privacy.RedactValue, true, nil
	}
	switch typed := value.(type) {
	case string:
		if len(typed) > privacy.MaxValueLength {
			emitMetadataDrop(privacy, keyPath, "truncated")
			runes := []rune(typed)
			if len(runes) > privacy.MaxValueLength {
				return string(runes[:privacy.MaxValueLength]) + "…", true, nil
			}
		}
		return typed, true, nil
	case bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, json.Number:
		return typed, true, nil
	case time.Time:
		return typed.UTC().Format(time.RFC3339Nano), true, nil
	}
	normalized, ok, err := normalizeComplexMetadata(value)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		emitMetadataDrop(privacy, keyPath, "unsupported_value")
		return nil, false, nil
	}
	switch typed := normalized.(type) {
	case []any:
		items := make([]any, 0, len(typed))
		for index, item := range typed {
			sanitized, keep, err := sanitizeMetadataValue(item, fmt.Sprintf("%s[%d]", keyPath, index), key, privacy)
			if err != nil {
				return nil, false, err
			}
			if keep {
				items = append(items, sanitized)
			}
		}
		if len(items) == 0 {
			return nil, false, nil
		}
		return items, true, nil
	case map[string]any:
		sanitized, err := sanitizeCustomMetadata(typed, privacy, keyPath)
		if err != nil {
			return nil, false, err
		}
		if len(sanitized) == 0 {
			return nil, false, nil
		}
		return sanitized, true, nil
	default:
		return nil, false, nil
	}
}

func normalizeComplexMetadata(value any) (any, bool, error) {
	switch typed := value.(type) {
	case map[string]any:
		return typed, true, nil
	case []any:
		return typed, true, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, false, nil
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, false, nil
	}
	switch typed := decoded.(type) {
	case map[string]any, []any:
		return typed, true, nil
	default:
		return nil, false, nil
	}
}

func normalizeRuleKeys(values []string) map[string]struct{} {
	result := make(map[string]struct{})
	for _, value := range values {
		trimmed := strings.ToLower(strings.TrimSpace(value))
		if trimmed != "" {
			result[trimmed] = struct{}{}
		}
	}
	return result
}

func metadataRuleMatches(rules map[string]struct{}, keyPath string, key string) bool {
	_, okPath := rules[strings.ToLower(strings.TrimSpace(keyPath))]
	_, okKey := rules[strings.ToLower(strings.TrimSpace(key))]
	return okPath || okKey
}

func isSensitiveMetadataKey(keyPath string, key string) bool {
	candidates := []string{strings.ToLower(keyPath), strings.ToLower(key)}
	for _, pattern := range defaultSensitiveMetadataKeyPatterns {
		for _, candidate := range candidates {
			if strings.Contains(candidate, pattern) {
				return true
			}
		}
	}
	return false
}

func hashMetadataValue(value any) string {
	text := asString(value)
	if text == "" {
		body, err := json.Marshal(value)
		if err == nil {
			text = string(body)
		}
	}
	sum := sha256.Sum256([]byte(text))
	return "sha256_" + hex.EncodeToString(sum[:])
}

func emitMetadataDrop(privacy resolvedMetadataPrivacy, keyPath string, reason string) {
	if privacy.OnMetadataDrop != nil {
		privacy.OnMetadataDrop(MetadataDropInfo{
			KeyPath: keyPath,
			Reason:  reason,
			Mode:    privacy.Mode,
		})
	}
}

func shouldAttachDefaultOTLPAuthorization(otlpURL string) bool {
	parsed, err := url.Parse(otlpURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "cloptima.ai" || strings.HasSuffix(host, ".cloptima.ai")
}

func otlpAttributesFromPayload(payload map[string]any) []map[string]any {
	pairs := [][2]any{
		{"gen_ai.system", payload["provider"]},
		{"gen_ai.request.model", payload["model"]},
		{"gen_ai.response.model", payload["model"]},
		{"gen_ai.request.id", payload["request_id"]},
		{"gen_ai.response.id", payload["provider_request_id"]},
		{"source_event_id", payload["source_event_id"]},
		{"gen_ai.usage.input_tokens", payload["input_tokens"]},
		{"gen_ai.usage.output_tokens", payload["output_tokens"]},
		{"gen_ai.usage.total_tokens", payload["total_tokens"]},
		{"gen_ai.usage.reasoning_tokens", payload["reasoning_tokens"]},
		{"gen_ai.usage.cached_input_tokens", payload["cached_input_tokens"]},
		{"extra_usage_units", payload["extra_usage_units"]},
		{"gen_ai.usage.cost", payload["vendor_reported_cost_usd"]},
		{"cache_hit", payload["cache_hit"]},
		{"cloptima.request_id", payload["request_id"]},
		{"trace_id", payload["trace_id"]},
	}
	pairs = append(pairs, otlpExtraUsagePairs(payload)...)
	if metadata, ok := payload["metadata"].(map[string]any); ok {
		for key, value := range metadata {
			pairs = append(pairs, [2]any{key, value})
		}
	}
	seen := make(map[string]struct{})
	attributes := make([]map[string]any, 0, len(pairs))
	for _, pair := range pairs {
		key := strings.TrimSpace(asString(pair[0]))
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if value, ok := otlpAttributeValue(pair[1]); ok {
			attributes = append(attributes, map[string]any{
				"key":   key,
				"value": value,
			})
		}
	}
	return attributes
}

func otlpExtraUsagePairs(payload map[string]any) [][2]any {
	values := map[string]int{}
	switch typed := payload["extra_usage_units"].(type) {
	case map[string]int:
		for key, value := range typed {
			if value > 0 {
				values[strings.TrimSpace(key)] = value
			}
		}
	case map[string]any:
		for key, value := range typed {
			resolved := cleanNonNegativeIntValue(value)
			if resolved > 0 {
				values[strings.TrimSpace(key)] = resolved
			}
		}
	}
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		if key != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	pairs := make([][2]any, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, [2]any{"gen_ai.usage." + key, values[key]})
	}
	return pairs
}

func otlpAttributeValue(value any) (map[string]any, bool) {
	switch typed := value.(type) {
	case string:
		return map[string]any{"stringValue": typed}, true
	case bool:
		return map[string]any{"boolValue": typed}, true
	case int:
		return map[string]any{"intValue": typed}, true
	case int64:
		return map[string]any{"intValue": typed}, true
	case float64:
		return map[string]any{"doubleValue": typed}, true
	case float32:
		return map[string]any{"doubleValue": typed}, true
	case map[string]any, map[string]int, []any:
		body, err := json.Marshal(typed)
		if err != nil {
			return nil, false
		}
		return map[string]any{"stringValue": string(body)}, true
	default:
		return nil, false
	}
}

func extractPayloadEvents(payload map[string]any) []map[string]any {
	if payload == nil {
		return nil
	}
	if eventsRaw, ok := payload["events"]; ok {
		switch typed := eventsRaw.(type) {
		case []map[string]any:
			return typed
		case []any:
			events := make([]map[string]any, 0, len(typed))
			for _, item := range typed {
				if event, ok := item.(map[string]any); ok {
					events = append(events, event)
				}
			}
			return events
		}
	}
	return []map[string]any{payload}
}

func cleanString(value any, fallback string) string {
	trimmed := strings.TrimSpace(asString(value))
	if trimmed == "" {
		return fallback
	}
	return trimmed
}

func asString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	default:
		return fmt.Sprintf("%v", value)
	}
}

func randomHex(byteLength int) string {
	if byteLength <= 0 {
		byteLength = 16
	}
	buffer := make([]byte, byteLength)
	if _, err := rand.Read(buffer); err == nil {
		return hex.EncodeToString(buffer)
	}
	fallback := make([]byte, byteLength)
	for index := range fallback {
		fallback[index] = byte(mathrand.Intn(256))
	}
	return hex.EncodeToString(fallback)
}

func timePtrToAny(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func cleanNonNegativeIntPtr(value *int) *int {
	if value == nil || *value < 0 {
		return nil
	}
	return Int(*value)
}

func cleanNonNegativeIntValue(value any) int {
	switch typed := value.(type) {
	case int:
		if typed >= 0 {
			return typed
		}
	case int64:
		if typed >= 0 {
			return int(typed)
		}
	case float64:
		if typed >= 0 {
			return int(typed)
		}
	}
	return 0
}

func cleanCostValue(value any) any {
	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return nil
		}
		return trimmed
	case int, int64, float64, float32, json.Number:
		return typed
	default:
		return nil
	}
}

func unixNanoString(value any, fallback time.Time) string {
	switch typed := value.(type) {
	case string:
		if parsed, err := time.Parse(time.RFC3339Nano, typed); err == nil {
			return fmt.Sprintf("%d", parsed.UTC().UnixNano())
		}
		if parsed, err := time.Parse(time.RFC3339, typed); err == nil {
			return fmt.Sprintf("%d", parsed.UTC().UnixNano())
		}
	case time.Time:
		return fmt.Sprintf("%d", typed.UTC().UnixNano())
	}
	return fmt.Sprintf("%d", fallback.UTC().UnixNano())
}

func otlpStatusCode(value any) int {
	if strings.TrimSpace(asString(value)) == "failed" {
		return 2
	}
	return 1
}

func hasHeader(headers map[string]string, target string) bool {
	target = strings.ToLower(target)
	for key := range headers {
		if strings.ToLower(key) == target {
			return true
		}
	}
	return false
}

func optionStrings(options *MetadataPrivacyOptions, selector func(*MetadataPrivacyOptions) []string) []string {
	if options == nil {
		return nil
	}
	return selector(options)
}

func optionCallback(options *MetadataPrivacyOptions) func(MetadataDropInfo) {
	if options == nil {
		return nil
	}
	return options.OnMetadataDrop
}

func boolPtrToAny(value *bool) any {
	if value == nil {
		return nil
	}
	return *value
}

func intPtrToAny(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func emptyMapToNil(value map[string]int) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

func emptyToNil(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return strings.TrimSpace(value)
}

func stripNilValues(value map[string]any) map[string]any {
	result := make(map[string]any)
	for key, nested := range value {
		if nested != nil {
			result[key] = nested
		}
	}
	return result
}

func cloneStringMap(value map[string]string) map[string]string {
	if value == nil {
		return nil
	}
	result := make(map[string]string, len(value))
	for key, nested := range value {
		result[key] = nested
	}
	return result
}

func cloneAnyMap(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	result := make(map[string]any, len(value))
	for key, nested := range value {
		result[key] = nested
	}
	return result
}

func valueOrZero(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func statusOrDefault(value string) string {
	switch strings.TrimSpace(value) {
	case "failed", "partial", "blocked", "succeeded":
		return strings.TrimSpace(value)
	default:
		return "succeeded"
	}
}

func maxInt(value int, fallback int) int {
	if value < fallback {
		return fallback
	}
	return value
}

func applyExtractedUsage(event *UsageEvent, extracted ExtractedUsage) {
	if extracted.Provider != nil {
		event.Provider = *extracted.Provider
	}
	if extracted.Model != nil {
		event.Model = *extracted.Model
	}
	if extracted.RequestID != nil {
		event.RequestID = *extracted.RequestID
	}
	if extracted.ProviderRequestID != nil {
		event.ProviderRequestID = *extracted.ProviderRequestID
	}
	if extracted.TraceID != nil {
		event.TraceID = *extracted.TraceID
	}
	if extracted.Status != nil {
		event.Status = *extracted.Status
	}
	if extracted.InputTokens != nil {
		event.InputTokens = Int(*extracted.InputTokens)
	}
	if extracted.OutputTokens != nil {
		event.OutputTokens = Int(*extracted.OutputTokens)
	}
	if extracted.TotalTokens != nil {
		event.TotalTokens = Int(*extracted.TotalTokens)
	}
	if extracted.ReasoningTokens != nil {
		event.ReasoningTokens = Int(*extracted.ReasoningTokens)
	}
	if extracted.CachedInputTokens != nil {
		event.CachedInputTokens = Int(*extracted.CachedInputTokens)
	}
	if extracted.LatencyMS != nil {
		event.LatencyMS = Int(*extracted.LatencyMS)
	}
	if extracted.ExtraUsageUnits != nil {
		if event.ExtraUsageUnits == nil {
			event.ExtraUsageUnits = map[string]int{}
		}
		for key, value := range extracted.ExtraUsageUnits {
			event.ExtraUsageUnits[key] = value
		}
	}
	if extracted.CacheHit != nil {
		event.CacheHit = Bool(*extracted.CacheHit)
	}
	if extracted.VendorReportedCostUSD != nil {
		event.VendorReportedCostUSD = extracted.VendorReportedCostUSD
	}
	if extracted.ErrorMessage != nil {
		event.ErrorMessage = *extracted.ErrorMessage
	}
	if len(extracted.Metadata) > 0 {
		if event.Metadata == nil {
			event.Metadata = map[string]any{}
		}
		for key, value := range extracted.Metadata {
			event.Metadata[key] = value
		}
	}
}

func recordObservedEvent(ctx context.Context, client Client, event UsageEvent, privacy *MetadataPrivacyOptions, fireAndForget bool) error {
	type syncPolicyRecorder interface {
		recordWithPolicy(context.Context, UsageEvent, *MetadataPrivacyOptions) error
	}
	type asyncPolicyRecorder interface {
		recordAsyncWithPolicy(context.Context, UsageEvent, *MetadataPrivacyOptions)
	}
	if fireAndForget {
		if recorder, ok := client.(asyncPolicyRecorder); ok {
			recorder.recordAsyncWithPolicy(ctx, event, privacy)
			return nil
		}
		client.RecordAsync(ctx, event)
		return nil
	}
	if recorder, ok := client.(syncPolicyRecorder); ok {
		return recorder.recordWithPolicy(ctx, event, privacy)
	}
	return client.Record(ctx, event)
}

func reportObserveError(client Client, err error) {
	if err == nil {
		return
	}
	type observeErrorReporter interface {
		reportObserveError(error)
	}
	if reporter, ok := client.(observeErrorReporter); ok {
		reporter.reportObserveError(err)
	}
}

func metadataPrivacyOptionsFromResolved(value resolvedMetadataPrivacy) *MetadataPrivacyOptions {
	return &MetadataPrivacyOptions{
		Mode:               value.Mode,
		AllowlistKeys:      ruleKeysToSlice(value.AllowlistKeys),
		DenylistKeys:       ruleKeysToSlice(value.DenylistKeys),
		RedactKeys:         ruleKeysToSlice(value.RedactKeys),
		HashKeys:           ruleKeysToSlice(value.HashKeys),
		MaxKeys:            value.MaxKeys,
		MaxValueLength:     value.MaxValueLength,
		MaxSerializedBytes: value.MaxSerializedBytes,
		RedactValue:        value.RedactValue,
		OnMetadataDrop:     value.OnMetadataDrop,
	}
}

func ruleKeysToSlice(value map[string]struct{}) []string {
	if len(value) == 0 {
		return nil
	}
	result := make([]string, 0, len(value))
	for key := range value {
		result = append(result, key)
	}
	return result
}

func (c *observabilityClient) reportObserveError(err error) {
	if err != nil && c.onError != nil {
		c.onError(err)
	}
}

func (d *disabledClient) reportObserveError(_ error) {}
