package internal

import (
	"context"
	"fmt"
	"sort"

	goplugin "github.com/GoCodeAlone/go-plugin"
	pb "github.com/GoCodeAlone/workflow/plugin/external/proto"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

const (
	workflowPluginProtocolVersion = 1
	workflowPluginMagicCookieKey  = "WORKFLOW_PLUGIN"
	workflowPluginMagicCookieVal  = "workflow-external-plugin-v1"
)

var workflowHandshake = goplugin.HandshakeConfig{
	ProtocolVersion:  workflowPluginProtocolVersion,
	MagicCookieKey:   workflowPluginMagicCookieKey,
	MagicCookieValue: workflowPluginMagicCookieVal,
}

// ServeIaCPlugin serves the typed IaC provider through the Workflow go-plugin
// protocol expected by wfctl.
func ServeIaCPlugin(provider any) {
	goplugin.Serve(&goplugin.ServeConfig{
		HandshakeConfig: workflowHandshake,
		Plugins: goplugin.PluginSet{
			"iac": &iacGRPCPlugin{provider: provider},
		},
		GRPCServer: goplugin.DefaultGRPCServer,
	})
}

type iacGRPCPlugin struct {
	provider any
}

func (p *iacGRPCPlugin) GRPCServer(_ *goplugin.GRPCBroker, s *grpc.Server) error {
	return RegisterIaCProviderServices(s, p.provider)
}

func (p *iacGRPCPlugin) GRPCClient(_ context.Context, _ *goplugin.GRPCBroker, _ *grpc.ClientConn) (any, error) {
	return nil, nil
}

// RegisterIaCProviderServices registers the strict typed IaC services and the
// minimal PluginService bridge wfctl uses for contract discovery.
func RegisterIaCProviderServices(s *grpc.Server, provider any) error {
	if s == nil {
		return fmt.Errorf("RegisterIaCProviderServices: grpc server is nil")
	}
	required, ok := provider.(pb.IaCProviderRequiredServer)
	if !ok {
		return fmt.Errorf("RegisterIaCProviderServices: provider %T does not implement IaCProviderRequiredServer", provider)
	}
	pb.RegisterIaCProviderRequiredServer(s, required)
	if finalizer, ok := provider.(pb.IaCProviderFinalizerServer); ok {
		pb.RegisterIaCProviderFinalizerServer(s, finalizer)
	}
	pb.RegisterPluginServiceServer(s, &contractBridge{grpcSrv: s})
	return nil
}

type contractBridge struct {
	pb.UnimplementedPluginServiceServer
	grpcSrv *grpc.Server
}

func (b *contractBridge) GetContractRegistry(context.Context, *emptypb.Empty) (*pb.ContractRegistry, error) {
	return buildContractRegistry(b.grpcSrv), nil
}

func buildContractRegistry(grpcSrv *grpc.Server) *pb.ContractRegistry {
	registry := &pb.ContractRegistry{}
	if grpcSrv == nil {
		return registry
	}
	info := grpcSrv.GetServiceInfo()
	names := make([]string, 0, len(info))
	for name := range info {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		registry.Contracts = append(registry.Contracts, &pb.ContractDescriptor{
			Kind:        pb.ContractKind_CONTRACT_KIND_SERVICE,
			ServiceName: name,
			Mode:        pb.ContractMode_CONTRACT_MODE_STRICT_PROTO,
		})
	}
	return registry
}
