//go:build live_dns

// Env-gated live integration coverage for EnumerateAll("infra.dns").
//
// Run with:
//
//	INFRA_DNS_ENUMERATE_LIVE=1 CLOUDFLARE_API_TOKEN=$TOKEN \
//	  GOWORK=off go test -tags live_dns \
//	  -run TestCfProvider_EnumerateAll_DNS_live ./internal/...
//
// The build tag keeps this file out of `go test ./...` so the default
// suite stays hermetic. Per docs/plans/2026-05-26-dns-provider-contract.md
// PR 2 (Task 6).
package internal

import (
	"context"
	"os"
	"testing"

	"github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/option"
)

// newLiveCfProvider builds a cfProvider whose `zones` field is wired to
// the production cloudflare-go AutoPager. Credentials come from
// CLOUDFLARE_API_TOKEN; the helper aborts the test (t.Fatal) when the
// token is missing so the live-only run is loud rather than silent.
func newLiveCfProvider(t *testing.T) *cfProvider {
	t.Helper()
	token := os.Getenv("CLOUDFLARE_API_TOKEN")
	if token == "" {
		t.Fatal("CLOUDFLARE_API_TOKEN must be set for live EnumerateAll test")
	}
	return &cfProvider{
		zones: &cfRealZoneLister{client: cloudflare.NewClient(option.WithAPIToken(token))},
	}
}

func TestCfProvider_EnumerateAll_DNS_live(t *testing.T) {
	if os.Getenv("INFRA_DNS_ENUMERATE_LIVE") != "1" {
		t.Skip("set INFRA_DNS_ENUMERATE_LIVE=1 + CLOUDFLARE_API_TOKEN to run")
	}
	p := newLiveCfProvider(t)
	out, err := p.EnumerateAll(context.Background(), "infra.dns")
	if err != nil {
		t.Fatalf("live EnumerateAll: %v", err)
	}
	if len(out) == 0 {
		t.Skip("account has zero zones; cannot validate")
	}
	for _, o := range out {
		if o.ProviderID == "" {
			t.Errorf("empty zone ID for %+v", o.Outputs)
		}
		if o.Type != "infra.dns" {
			t.Errorf("wrong Type %q for %+v", o.Type, o.Outputs)
		}
		if _, ok := o.Outputs["zone"]; !ok {
			t.Errorf("missing zone output: %+v", o.Outputs)
		}
		if _, ok := o.Outputs["account_id"]; !ok {
			t.Errorf("missing account_id output: %+v", o.Outputs)
		}
	}
	t.Logf("enumerated %d zones from live account", len(out))
}
