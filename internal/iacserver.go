package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/GoCodeAlone/workflow-plugin-cloudflare/internal/drivers"
	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/GoCodeAlone/workflow/platform"
	pb "github.com/GoCodeAlone/workflow/plugin/external/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var Version = "0.0.0"

type cfIaCServer struct {
	pb.UnimplementedIaCProviderRequiredServer
	pb.UnimplementedIaCProviderFinalizerServer

	cfg          Config
	dnsDriver    *drivers.DNSDriver
	domainDriver *drivers.DomainDriver
}

var (
	_ pb.IaCProviderRequiredServer  = (*cfIaCServer)(nil)
	_ pb.IaCProviderFinalizerServer = (*cfIaCServer)(nil)
)

func NewIaCServer() *cfIaCServer {
	return &cfIaCServer{}
}

func (s *cfIaCServer) Name(_ context.Context, _ *pb.NameRequest) (*pb.NameResponse, error) {
	return &pb.NameResponse{Name: "cloudflare"}, nil
}

func (s *cfIaCServer) Version(_ context.Context, _ *pb.VersionRequest) (*pb.VersionResponse, error) {
	return &pb.VersionResponse{Version: Version}, nil
}

func (s *cfIaCServer) Capabilities(_ context.Context, _ *pb.CapabilitiesRequest) (*pb.CapabilitiesResponse, error) {
	return &pb.CapabilitiesResponse{
		Capabilities: []*pb.IaCCapabilityDeclaration{
			{
				ResourceType: "infra.dns",
				Tier:         1,
				Operations:   []string{"create", "read", "update", "delete"},
			},
			{
				ResourceType: "infra.domain",
				Tier:         1,
				Operations:   []string{"read", "update"},
			},
		},
		ComputePlanVersion: "v2",
	}, nil
}

func (s *cfIaCServer) Initialize(_ context.Context, req *pb.InitializeRequest) (*pb.InitializeResponse, error) {
	m, err := unmarshalJSONMap(req.GetConfigJson())
	if err != nil {
		return nil, fmt.Errorf("cloudflare iacserver: parse config_json: %w", err)
	}
	cfg := Config{APIToken: strVal(m, "api_token"), AccountID: strVal(m, "account_id")}
	if cfg.APIToken == "" {
		cfg.APIToken = strVal(m, "token")
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("cloudflare iacserver: invalid config: %w", err)
	}
	s.cfg = cfg
	s.dnsDriver = drivers.NewDNSDriver(cfg.APIToken)
	s.domainDriver = drivers.NewDomainDriver(cfg.APIToken, cfg.AccountID)
	return &pb.InitializeResponse{}, nil
}

func (s *cfIaCServer) Plan(ctx context.Context, req *pb.PlanRequest) (*pb.PlanResponse, error) {
	if s.dnsDriver == nil || s.domainDriver == nil {
		return nil, fmt.Errorf("cloudflare iacserver: Plan called before Initialize")
	}
	desired, err := specsFromPB(req.GetDesired())
	if err != nil {
		return nil, fmt.Errorf("cloudflare iacserver: decode Plan desired: %w", err)
	}
	current, err := statesFromPB(req.GetCurrent())
	if err != nil {
		return nil, fmt.Errorf("cloudflare iacserver: decode Plan current: %w", err)
	}
	p := &cfProvider{dnsDriver: s.dnsDriver, domainDriver: s.domainDriver}
	plan, err := platform.ComputePlan(ctx, p, desired, current)
	if err != nil {
		return nil, err
	}
	pbPlan, err := planToPB(&plan)
	if err != nil {
		return nil, fmt.Errorf("cloudflare iacserver: encode Plan response: %w", err)
	}
	return &pb.PlanResponse{Plan: pbPlan}, nil
}

func (s *cfIaCServer) Destroy(ctx context.Context, req *pb.DestroyRequest) (*pb.DestroyResponse, error) {
	if s.dnsDriver == nil || s.domainDriver == nil {
		return nil, fmt.Errorf("cloudflare iacserver: Destroy called before Initialize")
	}
	refs := refsFromPB(req.GetRefs())
	var destroyed []string
	var errs []*pb.ActionError
	for _, ref := range refs {
		driver, err := s.resourceDriver(ref.Type)
		if err == nil {
			err = driver.Delete(ctx, ref)
		}
		if err != nil {
			errs = append(errs, &pb.ActionError{Resource: ref.Name, Action: "delete", Error: err.Error()})
		} else {
			destroyed = append(destroyed, ref.Name)
		}
	}
	return &pb.DestroyResponse{Result: &pb.DestroyResult{Destroyed: destroyed, Errors: errs}}, nil
}

func (s *cfIaCServer) Status(ctx context.Context, req *pb.StatusRequest) (*pb.StatusResponse, error) {
	if s.dnsDriver == nil || s.domainDriver == nil {
		return nil, fmt.Errorf("cloudflare iacserver: Status called before Initialize")
	}
	refs := refsFromPB(req.GetRefs())
	statuses := make([]*pb.ResourceStatus, 0, len(refs))
	for _, ref := range refs {
		driver, err := s.resourceDriver(ref.Type)
		if err != nil {
			statuses = append(statuses, &pb.ResourceStatus{Name: ref.Name, Type: ref.Type, Status: "error"})
			continue
		}
		out, err := driver.Read(ctx, ref)
		if err != nil {
			statuses = append(statuses, &pb.ResourceStatus{Name: ref.Name, Type: ref.Type, Status: "error"})
			continue
		}
		outputsJSON, err := json.Marshal(out.Outputs)
		if err != nil {
			statuses = append(statuses, &pb.ResourceStatus{Name: out.Name, Type: out.Type, ProviderId: out.ProviderID, Status: "error"})
			continue
		}
		statuses = append(statuses, &pb.ResourceStatus{
			Name:        out.Name,
			Type:        out.Type,
			ProviderId:  out.ProviderID,
			Status:      out.Status,
			OutputsJson: outputsJSON,
		})
	}
	return &pb.StatusResponse{Statuses: statuses}, nil
}

func (s *cfIaCServer) Import(ctx context.Context, req *pb.ImportRequest) (*pb.ImportResponse, error) {
	if s.dnsDriver == nil || s.domainDriver == nil {
		return nil, fmt.Errorf("cloudflare iacserver: Import called before Initialize")
	}
	resourceType := req.GetResourceType()
	if resourceType == "" {
		resourceType = "infra.dns"
	}
	ref := interfaces.ResourceRef{
		Name:       req.GetProviderId(),
		Type:       resourceType,
		ProviderID: req.GetProviderId(),
	}
	driver, err := s.resourceDriver(resourceType)
	if err != nil {
		return nil, err
	}
	out, err := driver.Read(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("cloudflare iacserver: import %q: %w", req.GetProviderId(), err)
	}
	outputsJSON, err := json.Marshal(out.Outputs)
	if err != nil {
		return nil, fmt.Errorf("cloudflare iacserver: marshal import outputs: %w", err)
	}
	appliedConfig := importedAppliedConfig(out.Type, out.Outputs)
	appliedConfigJSON, err := json.Marshal(appliedConfig)
	if err != nil {
		return nil, fmt.Errorf("cloudflare iacserver: marshal import applied config: %w", err)
	}
	now := time.Now()
	return &pb.ImportResponse{State: &pb.ResourceState{
		Id:                  out.ProviderID,
		Name:                out.Name,
		Type:                out.Type,
		Provider:            "cloudflare",
		ProviderId:          out.ProviderID,
		AppliedConfigJson:   appliedConfigJSON,
		AppliedConfigSource: "adoption",
		OutputsJson:         outputsJSON,
		CreatedAt:           timestamppb.New(now),
		UpdatedAt:           timestamppb.New(now),
	}}, nil
}

func (s *cfIaCServer) resourceDriver(resourceType string) (interfaces.ResourceDriver, error) {
	switch resourceType {
	case "", "infra.dns":
		return s.dnsDriver, nil
	case "infra.domain":
		return s.domainDriver, nil
	default:
		return nil, fmt.Errorf("cloudflare: unsupported resource type %q", resourceType)
	}
}

func (s *cfIaCServer) ResolveSizing(_ context.Context, _ *pb.ResolveSizingRequest) (*pb.ResolveSizingResponse, error) {
	return &pb.ResolveSizingResponse{Sizing: nil}, nil
}

func (s *cfIaCServer) BootstrapStateBackend(_ context.Context, _ *pb.BootstrapStateBackendRequest) (*pb.BootstrapStateBackendResponse, error) {
	return &pb.BootstrapStateBackendResponse{Result: nil}, nil
}

func (s *cfIaCServer) FinalizeApply(_ context.Context, _ *pb.FinalizeApplyRequest) (*pb.FinalizeApplyResponse, error) {
	return &pb.FinalizeApplyResponse{}, nil
}

type cfProvider struct {
	dnsDriver    *drivers.DNSDriver
	domainDriver *drivers.DomainDriver
}

func (p *cfProvider) Name() string    { return "cloudflare" }
func (p *cfProvider) Version() string { return Version }
func (p *cfProvider) Initialize(_ context.Context, _ map[string]any) error {
	return nil
}
func (p *cfProvider) Capabilities() []interfaces.IaCCapabilityDeclaration {
	return []interfaces.IaCCapabilityDeclaration{
		{ResourceType: "infra.dns", Tier: 1, Operations: []string{"create", "read", "update", "delete"}},
		{ResourceType: "infra.domain", Tier: 1, Operations: []string{"read", "update"}},
	}
}
func (p *cfProvider) ResourceDriver(resourceType string) (interfaces.ResourceDriver, error) {
	switch resourceType {
	case "", "infra.dns":
		return p.dnsDriver, nil
	case "infra.domain":
		return p.domainDriver, nil
	default:
		return nil, fmt.Errorf("cloudflare: unsupported resource type %q", resourceType)
	}
}
func (p *cfProvider) Plan(ctx context.Context, desired []interfaces.ResourceSpec, current []interfaces.ResourceState) (*interfaces.IaCPlan, error) {
	plan, err := platform.ComputePlan(ctx, p, desired, current)
	return &plan, err
}
func (p *cfProvider) Destroy(_ context.Context, _ []interfaces.ResourceRef) (*interfaces.DestroyResult, error) {
	return &interfaces.DestroyResult{}, nil
}
func (p *cfProvider) Status(_ context.Context, _ []interfaces.ResourceRef) ([]interfaces.ResourceStatus, error) {
	return nil, nil
}
func (p *cfProvider) DetectDrift(_ context.Context, _ []interfaces.ResourceRef) ([]interfaces.DriftResult, error) {
	return nil, nil
}
func (p *cfProvider) Import(ctx context.Context, cloudID string, resourceType string) (*interfaces.ResourceState, error) {
	if resourceType == "" {
		resourceType = "infra.dns"
	}
	d, err := p.ResourceDriver(resourceType)
	if err != nil {
		return nil, err
	}
	out, err := d.Read(ctx, interfaces.ResourceRef{Name: cloudID, Type: resourceType, ProviderID: cloudID})
	if err != nil {
		return nil, err
	}
	now := time.Now()
	return &interfaces.ResourceState{
		ID:                  out.ProviderID,
		Name:                out.Name,
		Type:                out.Type,
		Provider:            "cloudflare",
		ProviderID:          out.ProviderID,
		AppliedConfig:       importedAppliedConfig(out.Type, out.Outputs),
		AppliedConfigSource: "adoption",
		Outputs:             out.Outputs,
		CreatedAt:           now,
		UpdatedAt:           now,
	}, nil
}
func (p *cfProvider) ResolveSizing(_ string, _ interfaces.Size, _ *interfaces.ResourceHints) (*interfaces.ProviderSizing, error) {
	return nil, nil
}
func (p *cfProvider) SupportedCanonicalKeys() []string { return interfaces.CanonicalKeys() }
func (p *cfProvider) BootstrapStateBackend(_ context.Context, _ map[string]any) (*interfaces.BootstrapResult, error) {
	return nil, nil
}
func (p *cfProvider) Close() error { return nil }

func unmarshalJSONMap(b []byte) (map[string]any, error) {
	if len(b) == 0 {
		return nil, nil
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func marshalJSONAny(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	return json.Marshal(v)
}

func strVal(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, _ := m[key].(string)
	return v
}

func refFromPB(r *pb.ResourceRef) interfaces.ResourceRef {
	if r == nil {
		return interfaces.ResourceRef{}
	}
	return interfaces.ResourceRef{Name: r.GetName(), Type: r.GetType(), ProviderID: r.GetProviderId()}
}

func refsFromPB(refs []*pb.ResourceRef) []interfaces.ResourceRef {
	out := make([]interfaces.ResourceRef, 0, len(refs))
	for _, r := range refs {
		out = append(out, refFromPB(r))
	}
	return out
}

func hintsToPB(h *interfaces.ResourceHints) *pb.ResourceHints {
	if h == nil {
		return nil
	}
	return &pb.ResourceHints{Cpu: h.CPU, Memory: h.Memory, Storage: h.Storage}
}

func hintsFromPB(h *pb.ResourceHints) *interfaces.ResourceHints {
	if h == nil {
		return nil
	}
	return &interfaces.ResourceHints{CPU: h.GetCpu(), Memory: h.GetMemory(), Storage: h.GetStorage()}
}

func specFromPB(s *pb.ResourceSpec) (interfaces.ResourceSpec, error) {
	if s == nil {
		return interfaces.ResourceSpec{}, nil
	}
	cfg, err := unmarshalJSONMap(s.GetConfigJson())
	if err != nil {
		return interfaces.ResourceSpec{}, err
	}
	return interfaces.ResourceSpec{
		Name:      s.GetName(),
		Type:      s.GetType(),
		Config:    cfg,
		Size:      interfaces.Size(s.GetSize()),
		Hints:     hintsFromPB(s.GetHints()),
		DependsOn: append([]string(nil), s.GetDependsOn()...),
	}, nil
}

func specsFromPB(specs []*pb.ResourceSpec) ([]interfaces.ResourceSpec, error) {
	out := make([]interfaces.ResourceSpec, 0, len(specs))
	for _, s := range specs {
		gs, err := specFromPB(s)
		if err != nil {
			return nil, err
		}
		out = append(out, gs)
	}
	return out, nil
}

func stateFromPB(s *pb.ResourceState) (*interfaces.ResourceState, error) {
	if s == nil {
		return nil, nil
	}
	applied, err := unmarshalJSONMap(s.GetAppliedConfigJson())
	if err != nil {
		return nil, err
	}
	outputs, err := unmarshalJSONMap(s.GetOutputsJson())
	if err != nil {
		return nil, err
	}
	return &interfaces.ResourceState{
		ID:                  s.GetId(),
		Name:                s.GetName(),
		Type:                s.GetType(),
		Provider:            s.GetProvider(),
		ProviderRef:         s.GetProviderRef(),
		ProviderID:          s.GetProviderId(),
		ConfigHash:          s.GetConfigHash(),
		AppliedConfig:       applied,
		AppliedConfigSource: s.GetAppliedConfigSource(),
		Outputs:             outputs,
		Dependencies:        append([]string(nil), s.GetDependencies()...),
		CreatedAt:           timeFromPB(s.GetCreatedAt()),
		UpdatedAt:           timeFromPB(s.GetUpdatedAt()),
	}, nil
}

func statesFromPB(states []*pb.ResourceState) ([]interfaces.ResourceState, error) {
	out := make([]interfaces.ResourceState, 0, len(states))
	for _, s := range states {
		gs, err := stateFromPB(s)
		if err != nil {
			return nil, err
		}
		if gs != nil {
			out = append(out, *gs)
		}
	}
	return out, nil
}

func changesToPB(changes []interfaces.FieldChange) ([]*pb.FieldChange, error) {
	out := make([]*pb.FieldChange, 0, len(changes))
	for _, c := range changes {
		oldJSON, err := marshalJSONAny(c.Old)
		if err != nil {
			return nil, err
		}
		newJSON, err := marshalJSONAny(c.New)
		if err != nil {
			return nil, err
		}
		out = append(out, &pb.FieldChange{Path: c.Path, OldJson: oldJSON, NewJson: newJSON, ForceNew: c.ForceNew})
	}
	return out, nil
}

func actionToPB(a interfaces.PlanAction) (*pb.PlanAction, error) {
	resource, err := specToPB(a.Resource)
	if err != nil {
		return nil, err
	}
	var current *pb.ResourceState
	if a.Current != nil {
		current, err = stateToPB(a.Current)
		if err != nil {
			return nil, err
		}
	}
	changes, err := changesToPB(a.Changes)
	if err != nil {
		return nil, err
	}
	return &pb.PlanAction{
		Action:             a.Action,
		Resource:           resource,
		Current:            current,
		Changes:            changes,
		ResolvedConfigHash: a.ResolvedConfigHash,
	}, nil
}

func specToPB(s interfaces.ResourceSpec) (*pb.ResourceSpec, error) {
	cfgJSON, err := json.Marshal(s.Config)
	if err != nil {
		return nil, err
	}
	return &pb.ResourceSpec{Name: s.Name, Type: s.Type, ConfigJson: cfgJSON, Size: string(s.Size), Hints: hintsToPB(s.Hints), DependsOn: append([]string(nil), s.DependsOn...)}, nil
}

func stateToPB(st *interfaces.ResourceState) (*pb.ResourceState, error) {
	if st == nil {
		return nil, nil
	}
	appliedJSON, err := json.Marshal(st.AppliedConfig)
	if err != nil {
		return nil, err
	}
	outputsJSON, err := json.Marshal(st.Outputs)
	if err != nil {
		return nil, err
	}
	return &pb.ResourceState{
		Id:                  st.ID,
		Name:                st.Name,
		Type:                st.Type,
		Provider:            st.Provider,
		ProviderRef:         st.ProviderRef,
		ProviderId:          st.ProviderID,
		ConfigHash:          st.ConfigHash,
		AppliedConfigJson:   appliedJSON,
		AppliedConfigSource: st.AppliedConfigSource,
		OutputsJson:         outputsJSON,
		Dependencies:        append([]string(nil), st.Dependencies...),
		CreatedAt:           timeToPB(st.CreatedAt),
		UpdatedAt:           timeToPB(st.UpdatedAt),
	}, nil
}

func planToPB(plan *interfaces.IaCPlan) (*pb.IaCPlan, error) {
	if plan == nil {
		return nil, nil
	}
	actions := make([]*pb.PlanAction, 0, len(plan.Actions))
	for _, action := range plan.Actions {
		pbAction, err := actionToPB(action)
		if err != nil {
			return nil, err
		}
		actions = append(actions, pbAction)
	}
	if plan.SchemaVersion < math.MinInt32 || plan.SchemaVersion > math.MaxInt32 {
		return nil, fmt.Errorf("cloudflare iacserver: plan SchemaVersion %d out of int32 range", plan.SchemaVersion)
	}
	return &pb.IaCPlan{
		Id:            plan.ID,
		Actions:       actions,
		CreatedAt:     timeToPB(plan.CreatedAt),
		DesiredHash:   plan.DesiredHash,
		SchemaVersion: int32(plan.SchemaVersion), //nolint:gosec // range-checked above
		InputSnapshot: copyStringMap(plan.InputSnapshot),
	}, nil
}

func importedAppliedConfig(resourceType string, outputs map[string]any) map[string]any {
	cfg := map[string]any{"provider": "cloudflare"}
	switch resourceType {
	case "infra.domain":
		copyIfPresent(cfg, outputs, "domain")
		copyIfPresent(cfg, outputs, "account_id")
	default:
		copyIfPresent(cfg, outputs, "domain")
		copyIfPresent(cfg, outputs, "zone_id")
		cfg["records"] = importedDNSRecords(outputs["records"])
	}
	return cfg
}

func importedDNSRecords(raw any) []map[string]any {
	rawRecords := recordsAsMaps(raw)
	records := make([]map[string]any, 0, len(rawRecords))
	for _, rawRecord := range rawRecords {
		record := make(map[string]any)
		for _, key := range []string{"type", "name", "data", "ttl", "priority", "proxied", "comment"} {
			if value, ok := rawRecord[key]; ok {
				record[key] = value
			}
		}
		records = append(records, record)
	}
	return records
}

func recordsAsMaps(raw any) []map[string]any {
	switch typed := raw.(type) {
	case []map[string]any:
		return typed
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if record, ok := item.(map[string]any); ok {
				out = append(out, record)
			}
		}
		return out
	default:
		return nil
	}
}

func copyIfPresent(dst map[string]any, src map[string]any, key string) {
	if src == nil {
		return
	}
	if value, ok := src[key]; ok {
		dst[key] = value
	}
}

func copyStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func timeFromPB(ts *timestamppb.Timestamp) time.Time {
	if ts == nil {
		return time.Time{}
	}
	return ts.AsTime()
}

func timeToPB(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}
