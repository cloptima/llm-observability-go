package llmobservability

import (
	"context"
	"net"
	"net/http"
	"strings"
)

type RequestContextData struct {
	Attribution Attribution
	RequestID   string
	TraceID     string
	Metadata    map[string]any
}

type HTTPRequestContextOptions struct {
	Attribution     Attribution
	Metadata        map[string]any
	IncludeHeaders  []string
	RequestIDHeader string
	TraceIDHeader   string
	Route           string
}

type requestContextKey struct{}

func WithRequestContextData(ctx context.Context, data RequestContextData) context.Context {
	current := CurrentRequestContextData(ctx)
	merged := RequestContextData{
		Attribution: mergeAttribution(current.Attribution, data.Attribution),
		RequestID:   fallbackString(data.RequestID, current.RequestID),
		TraceID:     fallbackString(data.TraceID, current.TraceID),
		Metadata:    mergeMetadata(current.Metadata, data.Metadata),
	}
	return context.WithValue(ctx, requestContextKey{}, merged)
}

func CurrentRequestContextData(ctx context.Context) RequestContextData {
	if ctx == nil {
		return RequestContextData{}
	}
	if value, ok := ctx.Value(requestContextKey{}).(RequestContextData); ok {
		return value
	}
	return RequestContextData{}
}

func InstrumentHTTPRequestContext(request *http.Request, options HTTPRequestContextOptions) RequestContextData {
	if request == nil {
		return RequestContextData{Attribution: options.Attribution, Metadata: cloneAnyMap(options.Metadata)}
	}
	path := strings.TrimSpace(options.Route)
	if path == "" && request.URL != nil {
		path = strings.TrimSpace(request.URL.Path)
	}
	metadata := mergeMetadata(options.Metadata, map[string]any{
		"http_method": strings.ToUpper(strings.TrimSpace(request.Method)),
		"http_route":  emptyToNilString(path),
		"http_path":   emptyToNilString(path),
		"http_host":   requestHost(request),
		"client_ip":   requestClientIP(request),
		"user_agent":  emptyToNilString(strings.TrimSpace(request.Header.Get("User-Agent"))),
	})
	metadata = mergeMetadata(metadata, selectedHeaderMetadata(request.Header, options.IncludeHeaders))
	return RequestContextData{
		Attribution: options.Attribution,
		RequestID:   requestContextRequestID(request.Header, options.RequestIDHeader),
		TraceID:     requestContextTraceID(request.Header, options.TraceIDHeader),
		Metadata:    metadata,
	}
}

func InstrumentHTTPTransportMetadata(request *http.Request, options HTTPRequestContextOptions) RequestContextData {
	data := InstrumentHTTPRequestContext(request, options)
	if request != nil && request.URL != nil {
		data.Metadata = mergeMetadata(data.Metadata, map[string]any{
			"provider_endpoint": request.URL.String(),
		})
	}
	return data
}

func RequestContextMiddleware(next http.Handler, options HTTPRequestContextOptions) http.Handler {
	if next == nil {
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		ctx := WithRequestContextData(request.Context(), InstrumentHTTPRequestContext(request, options))
		next.ServeHTTP(writer, request.WithContext(ctx))
	})
}

func requestContextRequestID(headers http.Header, requestIDHeader string) string {
	headerName := strings.TrimSpace(requestIDHeader)
	if headerName == "" {
		headerName = "x-request-id"
	}
	return strings.TrimSpace(headers.Get(headerName))
}

func requestContextTraceID(headers http.Header, traceIDHeader string) string {
	headerName := strings.TrimSpace(traceIDHeader)
	if headerName == "" {
		headerName = "x-trace-id"
	}
	if value := strings.TrimSpace(headers.Get(headerName)); value != "" {
		return value
	}
	return strings.TrimSpace(headers.Get("traceparent"))
}

func requestHost(request *http.Request) string {
	if request == nil {
		return ""
	}
	if request.URL != nil && strings.TrimSpace(request.URL.Host) != "" {
		return strings.TrimSpace(request.URL.Host)
	}
	return strings.TrimSpace(request.Host)
}

func requestClientIP(request *http.Request) string {
	if request == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(request.RemoteAddr))
	if err == nil {
		return strings.TrimSpace(host)
	}
	return strings.TrimSpace(request.RemoteAddr)
}

func emptyToNilString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return strings.TrimSpace(value)
}
