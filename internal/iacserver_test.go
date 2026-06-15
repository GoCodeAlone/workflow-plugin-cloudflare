package internal

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/GoCodeAlone/workflow-plugin-cloudflare/internal/drivers"
	pb "github.com/GoCodeAlone/workflow/plugin/external/proto"
	sdk "github.com/GoCodeAlone/workflow/plugin/external/sdk"
	"github.com/cloudflare/cloudflare-go/v7/zones"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

const (
	testBufSize     = 1024 * 1024
	testRPCDeadline = 5 * time.Second
)

func setupTestServer(t *testing.T) *grpc.ClientConn {
	t.Helper()
	listener := bufconn.Listen(testBufSize)
	t.Cleanup(func() { _ = listener.Close() })
	server := grpc.NewServer()
	srv := NewIaCServer()
	pb.RegisterIaCProviderRequiredServer(server, srv)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestCfIaCServer_NameVersionCapabilities(t *testing.T) {
	conn := setupTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), testRPCDeadline)
	t.Cleanup(cancel)
	client := pb.NewIaCProviderRequiredClient(conn)
	name, err := client.Name(ctx, &pb.NameRequest{})
	if err != nil {
		t.Fatalf("Name: %v", err)
	}
	if name.GetName() != "cloudflare" {
		t.Fatalf("name = %q, want cloudflare", name.GetName())
	}
	caps, err := client.Capabilities(ctx, &pb.CapabilitiesRequest{})
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if caps.GetComputePlanVersion() != "v2" {
		t.Fatalf("ComputePlanVersion = %q, want v2", caps.GetComputePlanVersion())
	}
	if len(caps.GetCapabilities()) != 3 ||
		caps.GetCapabilities()[0].GetResourceType() != "infra.dns" ||
		caps.GetCapabilities()[1].GetResourceType() != "infra.domain" ||
		caps.GetCapabilities()[2].GetResourceType() != "infra.http_redirect" {
		t.Fatalf("capabilities = %#v", caps.GetCapabilities())
	}
}

func TestCfIaCServer_ResourceDriverServer_RoutesByResourceType(t *testing.T) {
	listener := bufconn.Listen(testBufSize)
	t.Cleanup(func() { _ = listener.Close() })

	server := grpc.NewServer()
	srv := NewIaCServer()
	if _, err := srv.Initialize(context.Background(), &pb.InitializeRequest{ConfigJson: []byte(`{"api_token":"fake-token-for-test"}`)}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := sdk.RegisterAllIaCProviderServices(server, srv); err != nil {
		t.Fatalf("RegisterAllIaCProviderServices: %v", err)
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), testRPCDeadline)
	t.Cleanup(cancel)

	client := pb.NewResourceDriverClient(conn)
	if _, err := client.SensitiveKeys(ctx, &pb.SensitiveKeysRequest{ResourceType: "infra.dns"}); err != nil {
		t.Fatalf("SensitiveKeys(infra.dns): %v", err)
	}

	_, err = client.SensitiveKeys(ctx, &pb.SensitiveKeysRequest{ResourceType: "infra.unknown_for_test"})
	if err == nil {
		t.Fatal("SensitiveKeys(infra.unknown_for_test): expected error, got nil")
	}
	if got := status.Code(err); got == codes.Unimplemented {
		t.Fatalf("SensitiveKeys unknown returned Unimplemented; ResourceDriver service was not registered: %v", err)
	}
	if !strings.Contains(err.Error(), "infra.unknown_for_test") {
		t.Fatalf("SensitiveKeys unknown error = %q, want unknown type in message", err.Error())
	}
}

func TestCfIaCServer_InitializeMissingToken(t *testing.T) {
	conn := setupTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), testRPCDeadline)
	t.Cleanup(cancel)
	client := pb.NewIaCProviderRequiredClient(conn)
	_, err := client.Initialize(ctx, &pb.InitializeRequest{ConfigJson: []byte(`{}`)})
	if err == nil {
		t.Fatal("expected missing token error")
	}
}

func TestCfIaCServer_PlanBeforeInitialize(t *testing.T) {
	conn := setupTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), testRPCDeadline)
	t.Cleanup(cancel)
	client := pb.NewIaCProviderRequiredClient(conn)
	_, err := client.Plan(ctx, &pb.PlanRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCfIaCServer_ImportUsesDriverOutput(t *testing.T) {
	srv := &cfIaCServer{
		dnsDriver:      drivers.NewDNSDriverWithClient(&serverFakeCFClient{}),
		domainDriver:   drivers.NewDomainDriverWithClient("acct", &serverFakeRegistrarClient{}),
		redirectDriver: drivers.NewRedirectDriverWithClient(&serverFakeRedirectClient{}),
	}
	resp, err := srv.Import(context.Background(), &pb.ImportRequest{ProviderId: "zone", ResourceType: "infra.dns"})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if resp.GetState().GetProvider() != "cloudflare" {
		t.Fatalf("provider = %q, want cloudflare", resp.GetState().GetProvider())
	}
	var outputs map[string]any
	if err := json.Unmarshal(resp.GetState().GetOutputsJson(), &outputs); err != nil {
		t.Fatalf("unmarshal outputs: %v", err)
	}
	if outputs["domain"] != "example.com" {
		t.Fatalf("outputs = %#v", outputs)
	}
	if resp.GetState().GetAppliedConfigSource() != "adoption" {
		t.Fatalf("applied_config_source = %q, want adoption", resp.GetState().GetAppliedConfigSource())
	}
	var applied map[string]any
	if err := json.Unmarshal(resp.GetState().GetAppliedConfigJson(), &applied); err != nil {
		t.Fatalf("unmarshal applied config: %v", err)
	}
	if applied["provider"] != "cloudflare" || applied["domain"] != "example.com" || applied["zone_id"] != "zone" {
		t.Fatalf("applied config = %#v, want provider/domain/zone_id", applied)
	}
	records, ok := applied["records"].([]any)
	if !ok || len(records) != 1 {
		t.Fatalf("applied records = %#v, want one record", applied["records"])
	}
	record, ok := records[0].(map[string]any)
	if !ok || record["id"] != nil || record["type"] != "TXT" || record["data"] != "imported" {
		t.Fatalf("applied record = %#v, want IaC-safe TXT record without provider id", records[0])
	}
}

func TestCfIaCServer_ImportDomainUsesRegistrarDriver(t *testing.T) {
	srv := &cfIaCServer{
		dnsDriver:      drivers.NewDNSDriverWithClient(&serverFakeCFClient{}),
		domainDriver:   drivers.NewDomainDriverWithClient("acct", &serverFakeRegistrarClient{}),
		redirectDriver: drivers.NewRedirectDriverWithClient(&serverFakeRedirectClient{}),
	}
	resp, err := srv.Import(context.Background(), &pb.ImportRequest{ProviderId: "example.com", ResourceType: "infra.domain"})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if resp.GetState().GetProviderId() != "example.com" || resp.GetState().GetType() != "infra.domain" {
		t.Fatalf("state = %#v", resp.GetState())
	}
	var outputs map[string]any
	if err := json.Unmarshal(resp.GetState().GetOutputsJson(), &outputs); err != nil {
		t.Fatalf("unmarshal outputs: %v", err)
	}
	if outputs["account_id"] != "acct" || outputs["auto_renew"] != true {
		t.Fatalf("outputs = %#v", outputs)
	}
	if resp.GetState().GetAppliedConfigSource() != "adoption" {
		t.Fatalf("applied_config_source = %q, want adoption", resp.GetState().GetAppliedConfigSource())
	}
	var applied map[string]any
	if err := json.Unmarshal(resp.GetState().GetAppliedConfigJson(), &applied); err != nil {
		t.Fatalf("unmarshal applied config: %v", err)
	}
	if applied["provider"] != "cloudflare" || applied["domain"] != "example.com" || applied["account_id"] != "acct" {
		t.Fatalf("applied config = %#v, want provider/domain/account_id", applied)
	}
	if _, ok := applied["auto_renew"]; ok {
		t.Fatalf("applied config = %#v, auto_renew must stay output-only unless user opts in", applied)
	}
}

func TestCfProvider_ImportBuildsAdoptionConfig(t *testing.T) {
	provider := &cfProvider{
		dnsDriver:      drivers.NewDNSDriverWithClient(&serverFakeCFClient{}),
		domainDriver:   drivers.NewDomainDriverWithClient("acct", &serverFakeRegistrarClient{}),
		redirectDriver: drivers.NewRedirectDriverWithClient(&serverFakeRedirectClient{}),
	}
	state, err := provider.Import(context.Background(), "zone", "infra.dns")
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if state.AppliedConfigSource != "adoption" {
		t.Fatalf("AppliedConfigSource = %q, want adoption", state.AppliedConfigSource)
	}
	if state.AppliedConfig["domain"] != "example.com" || state.AppliedConfig["zone_id"] != "zone" {
		t.Fatalf("AppliedConfig = %#v, want imported dns config", state.AppliedConfig)
	}
}

type serverFakeCFClient struct{}

func (serverFakeCFClient) GetZone(_ context.Context, _, _ string) (*drivers.Zone, error) {
	return &drivers.Zone{ID: "zone", Name: "example.com", Status: "active", NameServers: []string{"a.ns.cloudflare.com"}}, nil
}
func (serverFakeCFClient) CreateZone(_ context.Context, _, _ string) (*drivers.Zone, error) {
	return nil, nil
}
func (serverFakeCFClient) DeleteZone(_ context.Context, _ string) error { return nil }
func (serverFakeCFClient) ListRecords(_ context.Context, _ string) ([]drivers.Record, error) {
	return []drivers.Record{{ID: "rec", Type: "TXT", Name: "example.com", Data: "imported", TTL: 300}}, nil
}
func (serverFakeCFClient) CreateRecord(_ context.Context, _ string, _ drivers.Record) (*drivers.Record, error) {
	return nil, nil
}
func (serverFakeCFClient) UpdateRecord(_ context.Context, _, _ string, _ drivers.Record) (*drivers.Record, error) {
	return nil, nil
}
func (serverFakeCFClient) DeleteRecord(_ context.Context, _, _ string) error { return nil }
func (serverFakeCFClient) GetDNSSEC(_ context.Context, _ string) (*drivers.DNSSEC, error) {
	return nil, nil
}

type serverFakeRedirectClient struct{}

func (serverFakeRedirectClient) GetZone(_ context.Context, _, _ string) (*drivers.Zone, error) {
	return &drivers.Zone{ID: "zone", Name: "example.com", Status: "active"}, nil
}
func (serverFakeRedirectClient) GetRedirectRuleset(_ context.Context, zoneID string) (*drivers.RedirectRuleset, error) {
	return &drivers.RedirectRuleset{
		ID:     "ruleset",
		ZoneID: zoneID,
		Rules: []drivers.RedirectRule{{
			Ref:                 "workflow_redirect_example_com",
			Expression:          `(http.host eq "example.com")`,
			TargetURL:           "https://example.org",
			StatusCode:          301,
			PreserveQueryString: true,
			Enabled:             true,
		}},
	}, nil
}
func (serverFakeRedirectClient) CreateRedirectRuleset(_ context.Context, zoneID string, rules []drivers.RedirectRule) (*drivers.RedirectRuleset, error) {
	return &drivers.RedirectRuleset{ID: "ruleset", ZoneID: zoneID, Rules: rules}, nil
}
func (serverFakeRedirectClient) UpdateRedirectRuleset(_ context.Context, zoneID, rulesetID string, rules []drivers.RedirectRule) (*drivers.RedirectRuleset, error) {
	return &drivers.RedirectRuleset{ID: rulesetID, ZoneID: zoneID, Rules: rules}, nil
}

type serverFakeRegistrarClient struct{}

func (serverFakeRegistrarClient) GetRegistration(_ context.Context, _, domain string) (*drivers.Registration, error) {
	return &drivers.Registration{DomainName: domain, AutoRenew: true, Locked: true, PrivacyMode: "redaction", Status: "active"}, nil
}
func (serverFakeRegistrarClient) UpdateRegistrationAutoRenew(_ context.Context, _, _ string, _ bool) (*drivers.RegistrarWorkflowStatus, error) {
	return &drivers.RegistrarWorkflowStatus{State: "pending"}, nil
}
func (serverFakeRegistrarClient) GetRegistrationStatus(_ context.Context, _, _ string) (*drivers.RegistrarWorkflowStatus, error) {
	return nil, nil
}
func (serverFakeRegistrarClient) GetUpdateStatus(_ context.Context, _, _ string) (*drivers.RegistrarWorkflowStatus, error) {
	return nil, nil
}

// ── EnumerateAll(infra.dns) coverage ────────────────────────────────────────

// slicePager is a deterministic in-memory zonePager used to drive EnumerateAll
// tests without touching the real cloudflare-go AutoPager (which is hard to
// construct from a slice in unit tests).
type slicePager struct {
	items []zones.Zone
	i     int
	cur   zones.Zone
	err   error
}

func (p *slicePager) Next() bool {
	if p.i >= len(p.items) {
		return false
	}
	p.cur = p.items[p.i]
	p.i++
	return true
}

func (p *slicePager) Current() zones.Zone { return p.cur }
func (p *slicePager) Err() error          { return p.err }

type fakeZoneLister struct {
	items []zones.Zone
	err   error
}

func (f *fakeZoneLister) ListZones(_ context.Context, _ zones.ZoneListParams) zonePager {
	return &slicePager{items: f.items, err: f.err}
}

func TestCfProvider_EnumerateAll_DNS(t *testing.T) {
	ctx := context.Background()
	p := &cfProvider{
		zones: &fakeZoneLister{items: []zones.Zone{
			{ID: "zid-1", Name: "alpha.test", Account: zones.ZoneAccount{ID: "acct-1"}},
			{ID: "zid-2", Name: "beta.test", Account: zones.ZoneAccount{ID: "acct-1"}},
		}},
	}
	out, err := p.EnumerateAll(ctx, "infra.dns")
	if err != nil {
		t.Fatalf("EnumerateAll: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("want 2 zones; got %d", len(out))
	}
	if out[0].Name != "alpha.test" {
		t.Errorf("name[0] = %q; want alpha.test", out[0].Name)
	}
	if out[0].ProviderID != "alpha.test" {
		t.Errorf("providerID[0] = %q; want alpha.test", out[0].ProviderID)
	}
	if out[0].Type != "infra.dns" {
		t.Errorf("type[0] = %q; want infra.dns", out[0].Type)
	}
	if out[0].Outputs["zone"] != "alpha.test" {
		t.Errorf("zone[0] = %v; want alpha.test", out[0].Outputs["zone"])
	}
	if out[0].Outputs["account_id"] != "acct-1" {
		t.Errorf("account_id[0] = %v; want acct-1", out[0].Outputs["account_id"])
	}
	if out[0].Outputs["zone_id"] != "zid-1" {
		t.Errorf("zone_id[0] = %v; want zid-1", out[0].Outputs["zone_id"])
	}
	if out[1].ProviderID != "beta.test" || out[1].Outputs["zone"] != "beta.test" {
		t.Errorf("zone[1] mismatch: %+v", out[1])
	}
}

func TestCfProvider_EnumerateAll_DNS_uninitialized(t *testing.T) {
	p := &cfProvider{}
	_, err := p.EnumerateAll(context.Background(), "infra.dns")
	if err == nil {
		t.Fatalf("want uninitialized error; got nil")
	}
}

func TestCfProvider_EnumerateAll_DNS_unsupportedType(t *testing.T) {
	p := &cfProvider{zones: &fakeZoneLister{}}
	_, err := p.EnumerateAll(context.Background(), "infra.compute")
	if err == nil {
		t.Fatalf("want unsupported-type error; got nil")
	}
}

// TestCfIaCServer_EnumerateAll_DNS exercises the typed gRPC surface
// (cfIaCServer.EnumerateAll). The SDK auto-registers this service at plugin
// startup because cfIaCServer satisfies pb.IaCProviderEnumeratorServer; this
// test confirms the proto<->Go marshalling on the EnumerateAll path is
// correct (outputs_json round-trips zone/account_id/zone_id).
func TestCfIaCServer_EnumerateAll_DNS(t *testing.T) {
	srv := &cfIaCServer{
		zones: &fakeZoneLister{items: []zones.Zone{
			{ID: "zid-1", Name: "alpha.test", Account: zones.ZoneAccount{ID: "acct-1"}},
			{ID: "zid-2", Name: "beta.test", Account: zones.ZoneAccount{ID: "acct-1"}},
		}},
	}
	resp, err := srv.EnumerateAll(context.Background(), &pb.EnumerateAllRequest{ResourceType: "infra.dns"})
	if err != nil {
		t.Fatalf("EnumerateAll: %v", err)
	}
	if len(resp.GetOutputs()) != 2 {
		t.Fatalf("want 2 outputs; got %d", len(resp.GetOutputs()))
	}
	first := resp.GetOutputs()[0]
	if first.GetName() != "alpha.test" {
		t.Errorf("name = %q; want alpha.test", first.GetName())
	}
	if first.GetProviderId() != "alpha.test" {
		t.Errorf("providerID = %q; want alpha.test", first.GetProviderId())
	}
	if first.GetType() != "infra.dns" {
		t.Errorf("type = %q; want infra.dns", first.GetType())
	}
	var outputs map[string]any
	if err := json.Unmarshal(first.GetOutputsJson(), &outputs); err != nil {
		t.Fatalf("unmarshal outputs: %v", err)
	}
	if outputs["zone"] != "alpha.test" || outputs["account_id"] != "acct-1" || outputs["zone_id"] != "zid-1" {
		t.Errorf("outputs = %#v", outputs)
	}
}

func TestCfIaCServer_EnumerateAll_BeforeInitialize(t *testing.T) {
	srv := &cfIaCServer{}
	_, err := srv.EnumerateAll(context.Background(), &pb.EnumerateAllRequest{ResourceType: "infra.dns"})
	if err == nil {
		t.Fatalf("want before-Initialize error; got nil")
	}
}
