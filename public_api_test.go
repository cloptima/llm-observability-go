package llmobservability_test

import (
	"testing"
	"time"

	llmobservability "github.com/cloptima/llm-observability-go"
)

func TestPackageVersionConstant(t *testing.T) {
	t.Logf("PACKAGE_VERSION=%s", llmobservability.PackageVersion)
}

func TestPublicPackageSmoke(t *testing.T) {
	payload, err := llmobservability.PreviewEventPayload(llmobservability.UsageEvent{
		Provider:     "openai",
		Model:        "gpt-4o-mini",
		InputTokens:  llmobservability.Int(3),
		OutputTokens: llmobservability.Int(2),
	}, llmobservability.PreviewOptions{
		DefaultAttribution: llmobservability.Attribution{
			AppID:       "agent-api",
			Environment: "dev",
		},
		SDKVersion: llmobservability.PackageVersion,
	})
	if err != nil {
		t.Fatalf("preview event payload: %v", err)
	}
	if !llmobservability.ValidatePayload(payload).Valid {
		t.Fatalf("expected valid public preview payload")
	}
	if len(llmobservability.StrictFinopsMetadataKeys()) == 0 {
		t.Fatalf("expected exported strict finops metadata keys")
	}
	if llmobservability.Disabled(nil).IsEnabled() {
		t.Fatalf("expected disabled client to report disabled")
	}
	llmobservability.RegisterSignalShutdown(nil, time.Second)()
}
