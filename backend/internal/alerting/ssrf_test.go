package alerting

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestValidateWebhookURL_BlocksPrivateAndLoopbackLiterals is the
// creation-time half of T-08's fix: a destination that already names a
// disallowed address, as a literal IP (no DNS involved, so this proves the
// guard itself rather than depending on any real network access this
// sandbox may or may not have).
func TestValidateWebhookURL_BlocksPrivateAndLoopbackLiterals(t *testing.T) {
	blocked := []string{
		"http://127.0.0.1/hook",
		"http://127.0.0.1:8080/hook",
		"https://169.254.169.254/latest/meta-data/",          // AWS/GCP metadata
		"http://169.254.170.2/v2/credentials/some-task-role", // ECS task metadata
		"http://10.0.0.5/hook",
		"http://172.16.0.5/hook",
		"http://192.168.1.5/hook",
		"http://0.0.0.0/hook",
		"http://[::1]/hook",
		"http://[fe80::1]/hook",
		"http://[fc00::1]/hook",
		"http://224.0.0.1/hook",
	}
	for _, dest := range blocked {
		t.Run(dest, func(t *testing.T) {
			if err := ValidateWebhookURL(t.Context(), dest); !errors.Is(err, ErrWebhookDestinationBlocked) {
				t.Fatalf("expected %q to be blocked, got %v", dest, err)
			}
		})
	}
}

// TestValidateWebhookURL_AllowsPublicLooking proves the guard isn't
// overbroad: a syntactically valid public-looking destination, and one whose
// hostname simply doesn't resolve in this sandbox (no outbound DNS, or a
// reserved test TLD like .invalid), are both accepted at creation time —
// matching alertchannels_test.go's own long-standing "https://hooks.invalid/..."
// fixture, which must keep working.
func TestValidateWebhookURL_AllowsPublicLooking(t *testing.T) {
	allowed := []string{
		"https://hooks.example.invalid/T00/B00/a-fake-webhook-token",
		"https://hooks.slack.com/services/T00/B00/token",
		"http://8.8.8.8/hook",
	}
	for _, dest := range allowed {
		t.Run(dest, func(t *testing.T) {
			if err := ValidateWebhookURL(t.Context(), dest); err != nil {
				t.Fatalf("expected %q to be allowed, got %v", dest, err)
			}
		})
	}
}

// TestValidateWebhookURL_RejectsMalformedOrNonHTTP proves scheme/shape
// validation happens here too, not only address-range validation.
func TestValidateWebhookURL_RejectsMalformedOrNonHTTP(t *testing.T) {
	cases := []string{
		"not a url at all: %zz",
		"file:///etc/passwd",
		"gopher://127.0.0.1:25/",
		"ftp://example.invalid/hook",
		"",
	}
	for _, dest := range cases {
		t.Run(dest, func(t *testing.T) {
			if err := ValidateWebhookURL(t.Context(), dest); !errors.Is(err, ErrWebhookDestinationBlocked) {
				t.Fatalf("expected %q to be rejected, got %v", dest, err)
			}
		})
	}
}

// TestWebhookDispatcher_DispatchTimeGuardBlocksPrivateDestination is the
// dispatch-time half of T-08's fix: even a destination that was never
// checked by ValidateWebhookURL at all (simulating one that passed
// creation-time validation — e.g. because it resolved to a public address
// then — but points at a private address by the time delivery actually
// happens, the DNS-rebinding scenario) is refused at the real dial, not
// merely logged about afterward. AllowPrivateNetworks is left false (the
// production default) so this exercises the real guard, unlike every other
// test in dispatch_test.go.
func TestWebhookDispatcher_DispatchTimeGuardBlocksPrivateDestination(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := &WebhookDispatcher{
		Client:            &http.Client{},
		MaxAttempts:       3,
		BackoffBase:       5 * time.Millisecond,
		BackoffMax:        20 * time.Millisecond,
		PerAttemptTimeout: 2 * time.Second,
		// Deliberately NOT set — srv.URL is a real loopback address
		// (httptest always binds 127.0.0.1), so the default guard must
		// refuse to dial it exactly as it would refuse any other private
		// destination.
	}

	channel := Channel{ID: "ssrf-test-channel", Type: "webhook", destination: srv.URL}
	outcome := d.Dispatch(context.Background(), channel, DispatchRequest{IncidentID: 1, TargetID: "t", Kind: "opened"})

	if outcome.Confirmed {
		t.Fatal("expected the SSRF guard to block delivery to a loopback destination")
	}
	// Not retryable, so exactly one attempt — the guard doesn't need three
	// tries at asking the same forbidden address.
	if outcome.Attempts != 1 {
		t.Fatalf("expected exactly 1 (non-retried) attempt, got %d", outcome.Attempts)
	}
	if outcome.LastError == "" {
		t.Fatal("expected a non-empty last_error describing the block")
	}
}

// TestWebhookDispatcher_AllowPrivateNetworksOptOutStillDelivers proves the
// escape hatch this package's own tests rely on (fastDispatcher) genuinely
// restores real delivery, so the guard's existence doesn't silently break
// every other webhook dispatch test in this package.
func TestWebhookDispatcher_AllowPrivateNetworksOptOutStillDelivers(t *testing.T) {
	var hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := fastDispatcher() // AllowPrivateNetworks: true
	channel := Channel{ID: "ssrf-test-channel-allow", Type: "webhook", destination: srv.URL}
	outcome := d.Dispatch(context.Background(), channel, DispatchRequest{IncidentID: 1, TargetID: "t", Kind: "opened"})

	if !outcome.Confirmed || !hit {
		t.Fatalf("expected delivery to succeed with AllowPrivateNetworks set, got %+v (server hit=%v)", outcome, hit)
	}
}
