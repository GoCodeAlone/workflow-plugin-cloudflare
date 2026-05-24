package internal

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/GoCodeAlone/workflow-plugin-cloudflare/internal/drivers"
	pb "github.com/GoCodeAlone/workflow/plugin/external/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
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
	if len(caps.GetCapabilities()) != 2 || caps.GetCapabilities()[0].GetResourceType() != "infra.dns" || caps.GetCapabilities()[1].GetResourceType() != "infra.domain" {
		t.Fatalf("capabilities = %#v", caps.GetCapabilities())
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
		dnsDriver:    drivers.NewDNSDriverWithClient(&serverFakeCFClient{}),
		domainDriver: drivers.NewDomainDriverWithClient("acct", &serverFakeRegistrarClient{}),
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
		dnsDriver:    drivers.NewDNSDriverWithClient(&serverFakeCFClient{}),
		domainDriver: drivers.NewDomainDriverWithClient("acct", &serverFakeRegistrarClient{}),
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
		dnsDriver:    drivers.NewDNSDriverWithClient(&serverFakeCFClient{}),
		domainDriver: drivers.NewDomainDriverWithClient("acct", &serverFakeRegistrarClient{}),
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
