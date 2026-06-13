package internal

import (
	"context"
	"fmt"

	"github.com/GoCodeAlone/workflow/interfaces"
	pb "github.com/GoCodeAlone/workflow/plugin/external/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *cfIaCServer) resolveResourceDriver(resourceType string) (interfaces.ResourceDriver, error) {
	if resourceType == "" {
		return nil, status.Error(codes.InvalidArgument, "cloudflare ResourceDriver: resource_type is required")
	}
	if s.dnsDriver == nil || s.domainDriver == nil {
		return nil, status.Error(codes.FailedPrecondition, "cloudflare ResourceDriver: Initialize must be called before resource driver RPCs")
	}
	driver, err := s.resourceDriver(resourceType)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "cloudflare ResourceDriver: %v", err)
	}
	return driver, nil
}

func (s *cfIaCServer) Create(ctx context.Context, req *pb.ResourceCreateRequest) (*pb.ResourceCreateResponse, error) {
	driver, err := s.resolveResourceDriver(req.GetResourceType())
	if err != nil {
		return nil, err
	}
	spec, err := specFromPB(req.GetSpec())
	if err != nil {
		return nil, fmt.Errorf("cloudflare ResourceDriver(%s).Create: decode spec: %w", req.GetResourceType(), err)
	}
	out, err := driver.Create(ctx, spec)
	if err != nil {
		return nil, err
	}
	pbOut, err := outputToPB(out)
	if err != nil {
		return nil, fmt.Errorf("cloudflare ResourceDriver(%s).Create: encode response: %w", req.GetResourceType(), err)
	}
	return &pb.ResourceCreateResponse{Output: pbOut}, nil
}

func (s *cfIaCServer) Read(ctx context.Context, req *pb.ResourceReadRequest) (*pb.ResourceReadResponse, error) {
	driver, err := s.resolveResourceDriver(req.GetResourceType())
	if err != nil {
		return nil, err
	}
	out, err := driver.Read(ctx, refFromPB(req.GetRef()))
	if err != nil {
		return nil, err
	}
	pbOut, err := outputToPB(out)
	if err != nil {
		return nil, fmt.Errorf("cloudflare ResourceDriver(%s).Read: encode response: %w", req.GetResourceType(), err)
	}
	return &pb.ResourceReadResponse{Output: pbOut}, nil
}

func (s *cfIaCServer) Update(ctx context.Context, req *pb.ResourceUpdateRequest) (*pb.ResourceUpdateResponse, error) {
	driver, err := s.resolveResourceDriver(req.GetResourceType())
	if err != nil {
		return nil, err
	}
	spec, err := specFromPB(req.GetSpec())
	if err != nil {
		return nil, fmt.Errorf("cloudflare ResourceDriver(%s).Update: decode spec: %w", req.GetResourceType(), err)
	}
	out, err := driver.Update(ctx, refFromPB(req.GetRef()), spec)
	if err != nil {
		return nil, err
	}
	pbOut, err := outputToPB(out)
	if err != nil {
		return nil, fmt.Errorf("cloudflare ResourceDriver(%s).Update: encode response: %w", req.GetResourceType(), err)
	}
	return &pb.ResourceUpdateResponse{Output: pbOut}, nil
}

func (s *cfIaCServer) Delete(ctx context.Context, req *pb.ResourceDeleteRequest) (*pb.ResourceDeleteResponse, error) {
	driver, err := s.resolveResourceDriver(req.GetResourceType())
	if err != nil {
		return nil, err
	}
	if err := driver.Delete(ctx, refFromPB(req.GetRef())); err != nil {
		return nil, err
	}
	return &pb.ResourceDeleteResponse{}, nil
}

func (s *cfIaCServer) Diff(ctx context.Context, req *pb.ResourceDiffRequest) (*pb.ResourceDiffResponse, error) {
	driver, err := s.resolveResourceDriver(req.GetResourceType())
	if err != nil {
		return nil, err
	}
	desired, err := specFromPB(req.GetDesired())
	if err != nil {
		return nil, fmt.Errorf("cloudflare ResourceDriver(%s).Diff: decode desired: %w", req.GetResourceType(), err)
	}
	current, err := outputFromPB(req.GetCurrent())
	if err != nil {
		return nil, fmt.Errorf("cloudflare ResourceDriver(%s).Diff: decode current: %w", req.GetResourceType(), err)
	}
	result, err := driver.Diff(ctx, desired, current)
	if err != nil {
		return nil, err
	}
	pbResult, err := diffResultToPB(result)
	if err != nil {
		return nil, fmt.Errorf("cloudflare ResourceDriver(%s).Diff: encode response: %w", req.GetResourceType(), err)
	}
	return &pb.ResourceDiffResponse{Result: pbResult}, nil
}

func (s *cfIaCServer) Scale(ctx context.Context, req *pb.ResourceScaleRequest) (*pb.ResourceScaleResponse, error) {
	driver, err := s.resolveResourceDriver(req.GetResourceType())
	if err != nil {
		return nil, err
	}
	out, err := driver.Scale(ctx, refFromPB(req.GetRef()), int(req.GetReplicas()))
	if err != nil {
		return nil, err
	}
	pbOut, err := outputToPB(out)
	if err != nil {
		return nil, fmt.Errorf("cloudflare ResourceDriver(%s).Scale: encode response: %w", req.GetResourceType(), err)
	}
	return &pb.ResourceScaleResponse{Output: pbOut}, nil
}

func (s *cfIaCServer) HealthCheck(ctx context.Context, req *pb.ResourceHealthCheckRequest) (*pb.ResourceHealthCheckResponse, error) {
	driver, err := s.resolveResourceDriver(req.GetResourceType())
	if err != nil {
		return nil, err
	}
	result, err := driver.HealthCheck(ctx, refFromPB(req.GetRef()))
	if err != nil {
		return nil, err
	}
	return &pb.ResourceHealthCheckResponse{Result: healthResultToPB(result)}, nil
}

func (s *cfIaCServer) SensitiveKeys(_ context.Context, req *pb.SensitiveKeysRequest) (*pb.SensitiveKeysResponse, error) {
	driver, err := s.resolveResourceDriver(req.GetResourceType())
	if err != nil {
		return nil, err
	}
	return &pb.SensitiveKeysResponse{Keys: append([]string(nil), driver.SensitiveKeys()...)}, nil
}

func (s *cfIaCServer) Troubleshoot(ctx context.Context, req *pb.TroubleshootRequest) (*pb.TroubleshootResponse, error) {
	driver, err := s.resolveResourceDriver(req.GetResourceType())
	if err != nil {
		return nil, err
	}
	tr, ok := driver.(interfaces.Troubleshooter)
	if !ok {
		return nil, status.Errorf(codes.Unimplemented,
			"cloudflare ResourceDriver(%s).Troubleshoot: driver does not implement interfaces.Troubleshooter",
			req.GetResourceType())
	}
	diags, err := tr.Troubleshoot(ctx, refFromPB(req.GetRef()), req.GetFailureMsg())
	if err != nil {
		return nil, err
	}
	out := make([]*pb.Diagnostic, 0, len(diags))
	for _, d := range diags {
		out = append(out, &pb.Diagnostic{
			Id:     d.ID,
			Phase:  d.Phase,
			Cause:  d.Cause,
			At:     timeToPB(d.At),
			Detail: d.Detail,
		})
	}
	return &pb.TroubleshootResponse{Diagnostics: out}, nil
}

func outputToPB(out *interfaces.ResourceOutput) (*pb.ResourceOutput, error) {
	if out == nil {
		return nil, nil
	}
	outputsJSON, err := marshalJSONAny(out.Outputs)
	if err != nil {
		return nil, err
	}
	return &pb.ResourceOutput{
		Name:        out.Name,
		Type:        out.Type,
		ProviderId:  out.ProviderID,
		OutputsJson: outputsJSON,
		Sensitive:   copyBoolMap(out.Sensitive),
		Status:      out.Status,
	}, nil
}

func outputFromPB(out *pb.ResourceOutput) (*interfaces.ResourceOutput, error) {
	if out == nil {
		return nil, nil
	}
	outputs, err := unmarshalJSONMap(out.GetOutputsJson())
	if err != nil {
		return nil, err
	}
	return &interfaces.ResourceOutput{
		Name:       out.GetName(),
		Type:       out.GetType(),
		ProviderID: out.GetProviderId(),
		Outputs:    outputs,
		Sensitive:  copyBoolMap(out.GetSensitive()),
		Status:     out.GetStatus(),
	}, nil
}

func diffResultToPB(result *interfaces.DiffResult) (*pb.DiffResult, error) {
	if result == nil {
		return nil, nil
	}
	changes, err := changesToPB(result.Changes)
	if err != nil {
		return nil, err
	}
	return &pb.DiffResult{
		NeedsUpdate:  result.NeedsUpdate,
		NeedsReplace: result.NeedsReplace,
		Changes:      changes,
	}, nil
}

func healthResultToPB(result *interfaces.HealthResult) *pb.HealthResult {
	if result == nil {
		return nil
	}
	return &pb.HealthResult{Healthy: result.Healthy, Message: result.Message}
}

func copyBoolMap(in map[string]bool) map[string]bool {
	if in == nil {
		return nil
	}
	out := make(map[string]bool, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
