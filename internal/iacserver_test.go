package internal

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/GoCodeAlone/workflow-plugin-cloudflare/internal/drivers"
	pb "github.com/GoCodeAlone/workflow/plugin/external/proto"
	sdk "github.com/GoCodeAlone/workflow/plugin/external/sdk"
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
	if len(caps.GetCapabilities()) != 1 || caps.GetCapabilities()[0].GetResourceType() != "infra.dns" {
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
	srv := &cfIaCServer{driver: drivers.NewDNSDriverWithClient(&serverFakeCFClient{})}
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
