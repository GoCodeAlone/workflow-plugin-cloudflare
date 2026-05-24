package drivers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/GoCodeAlone/workflow/interfaces"
	cloudflare "github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/option"
	"github.com/cloudflare/cloudflare-go/v7/registrar"
)

const domainResourceType = "infra.domain"

// RegistrarClient is the testable subset of Cloudflare Registrar operations.
type RegistrarClient interface {
	GetRegistration(ctx context.Context, accountID, domain string) (*Registration, error)
	UpdateRegistrationAutoRenew(ctx context.Context, accountID, domain string, autoRenew bool) (*RegistrarWorkflowStatus, error)
	GetRegistrationStatus(ctx context.Context, accountID, domain string) (*RegistrarWorkflowStatus, error)
	GetUpdateStatus(ctx context.Context, accountID, domain string) (*RegistrarWorkflowStatus, error)
}

// Registration is the Cloudflare Registrar metadata preserved in state.
type Registration struct {
	DomainName  string
	AutoRenew   bool
	CreatedAt   time.Time
	ExpiresAt   time.Time
	Locked      bool
	PrivacyMode string
	Status      string
}

// RegistrarWorkflowStatus is a compact Cloudflare async workflow snapshot.
type RegistrarWorkflowStatus struct {
	State     string
	Completed bool
	CreatedAt time.Time
	UpdatedAt time.Time
	ErrorCode string
	Error     string
}

// DomainDriver manages imported Cloudflare Registrar domains.
type DomainDriver struct {
	accountID string
	client    RegistrarClient
}

func NewDomainDriver(apiToken, accountID string) *DomainDriver {
	return &DomainDriver{accountID: accountID, client: newSDKRegistrarClient(apiToken)}
}

func NewDomainDriverWithClient(accountID string, client RegistrarClient) *DomainDriver {
	return &DomainDriver{accountID: accountID, client: client}
}

func (d *DomainDriver) Create(_ context.Context, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	domain, _ := domainNameFromSpec(spec)
	if domain == "" {
		domain = spec.Name
	}
	return nil, fmt.Errorf("cloudflare domain create %q: infra.domain is import-first; domain purchase and transfer are intentionally unsupported", domain)
}

func (d *DomainDriver) Read(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.ResourceOutput, error) {
	domain := domainNameFromRef(ref)
	if domain == "" || !interfaces.ValidateProviderID(domain, interfaces.IDFormatDomainName) {
		return nil, fmt.Errorf("cloudflare domain read %q: provider id or name must be a valid domain name", ref.Name)
	}
	accountID, err := d.requireAccountID()
	if err != nil {
		return nil, fmt.Errorf("cloudflare domain read %q: %w", domain, err)
	}
	return d.readDomain(ctx, accountID, domain)
}

func (d *DomainDriver) readDomain(ctx context.Context, accountID, domain string) (*interfaces.ResourceOutput, error) {
	reg, err := d.client.GetRegistration(ctx, accountID, domain)
	if err != nil {
		return nil, fmt.Errorf("cloudflare domain read %q: %w", domain, err)
	}
	regStatus, _ := d.client.GetRegistrationStatus(ctx, accountID, domain)
	updateStatus, _ := d.client.GetUpdateStatus(ctx, accountID, domain)
	return domainOutput(accountID, reg, regStatus, updateStatus), nil
}

func (d *DomainDriver) Update(ctx context.Context, ref interfaces.ResourceRef, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	parsed, err := parseDomainSpec(spec, d.accountID)
	if err != nil {
		return nil, fmt.Errorf("cloudflare domain update %q: %w", ref.Name, err)
	}
	domain := domainNameFromRef(ref)
	if domain == "" {
		domain = parsed.Domain
	}
	if !strings.EqualFold(domain, parsed.Domain) {
		return nil, fmt.Errorf("cloudflare domain update %q: cannot change domain from %q to %q", ref.Name, domain, parsed.Domain)
	}
	current, err := d.client.GetRegistration(ctx, parsed.AccountID, domain)
	if err != nil {
		return nil, fmt.Errorf("cloudflare domain update %q: %w", domain, err)
	}
	if parsed.AutoRenew != nil && current.AutoRenew != *parsed.AutoRenew {
		if !parsed.AllowAutoRenewUpdate {
			return nil, fmt.Errorf("cloudflare domain update %q: auto_renew changes require allow_auto_renew_update: true", domain)
		}
		if _, err := d.client.UpdateRegistrationAutoRenew(ctx, parsed.AccountID, domain, *parsed.AutoRenew); err != nil {
			return nil, fmt.Errorf("cloudflare domain update %q: %w", domain, err)
		}
	}
	return d.readDomain(ctx, parsed.AccountID, domain)
}

func (d *DomainDriver) Delete(_ context.Context, ref interfaces.ResourceRef) error {
	domain := domainNameFromRef(ref)
	if domain == "" {
		domain = ref.Name
	}
	return fmt.Errorf("cloudflare domain delete %q: infra.domain refuses to delete registrar domains; remove state explicitly if unmanaged", domain)
}

func (d *DomainDriver) Diff(_ context.Context, desired interfaces.ResourceSpec, current *interfaces.ResourceOutput) (*interfaces.DiffResult, error) {
	if current == nil {
		domain, _ := domainNameFromSpec(desired)
		if domain == "" {
			domain = desired.Name
		}
		return nil, fmt.Errorf("cloudflare domain diff %q: infra.domain is import-first; import or adopt the existing registrar domain before planning updates", domain)
	}
	parsed, err := parseDomainSpec(desired, d.accountID)
	if err != nil {
		return nil, fmt.Errorf("cloudflare domain diff: %w", err)
	}
	currentDomain, _ := current.Outputs["domain"].(string)
	if currentDomain != "" && !strings.EqualFold(currentDomain, parsed.Domain) {
		return &interfaces.DiffResult{
			NeedsUpdate:  true,
			NeedsReplace: true,
			Changes: []interfaces.FieldChange{{
				Path:     "domain",
				Old:      currentDomain,
				New:      parsed.Domain,
				ForceNew: true,
			}},
		}, nil
	}
	currentAccountID, _ := current.Outputs["account_id"].(string)
	if currentAccountID != "" && parsed.AccountID != "" && currentAccountID != parsed.AccountID {
		return &interfaces.DiffResult{
			NeedsUpdate:  true,
			NeedsReplace: true,
			Changes: []interfaces.FieldChange{{
				Path:     "account_id",
				Old:      currentAccountID,
				New:      parsed.AccountID,
				ForceNew: true,
			}},
		}, nil
	}
	if parsed.AutoRenew == nil {
		return &interfaces.DiffResult{}, nil
	}
	currentAutoRenew, _ := current.Outputs["auto_renew"].(bool)
	if currentAutoRenew == *parsed.AutoRenew {
		return &interfaces.DiffResult{}, nil
	}
	return &interfaces.DiffResult{
		NeedsUpdate: true,
		Changes: []interfaces.FieldChange{{
			Path: "auto_renew",
			Old:  currentAutoRenew,
			New:  *parsed.AutoRenew,
		}},
	}, nil
}

func (d *DomainDriver) HealthCheck(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.HealthResult, error) {
	out, err := d.Read(ctx, ref)
	if err != nil {
		return &interfaces.HealthResult{Healthy: false, Message: err.Error()}, nil
	}
	return &interfaces.HealthResult{Healthy: out.Status == "active", Message: out.Status}, nil
}

func (d *DomainDriver) Scale(ctx context.Context, ref interfaces.ResourceRef, _ int) (*interfaces.ResourceOutput, error) {
	return d.Read(ctx, ref)
}

func (d *DomainDriver) Type() string { return domainResourceType }

func (d *DomainDriver) SensitiveKeys() []string { return nil }

func (d *DomainDriver) ProviderIDFormat() interfaces.ProviderIDFormat {
	return interfaces.IDFormatDomainName
}

func (d *DomainDriver) AdoptionRef(spec interfaces.ResourceSpec) (interfaces.ResourceRef, bool, error) {
	parsed, err := parseDomainSpec(spec, d.accountID)
	if err != nil {
		return interfaces.ResourceRef{}, false, err
	}
	return interfaces.ResourceRef{Name: parsed.Domain, Type: domainResourceType, ProviderID: parsed.Domain}, true, nil
}

func (d *DomainDriver) requireAccountID() (string, error) {
	if strings.TrimSpace(d.accountID) == "" {
		return "", fmt.Errorf("account_id is required for Cloudflare Registrar domains")
	}
	return d.accountID, nil
}

type domainSpec struct {
	Domain               string
	AccountID            string
	AutoRenew            *bool
	AllowAutoRenewUpdate bool
}

func parseDomainSpec(spec interfaces.ResourceSpec, defaultAccountID string) (domainSpec, error) {
	domain, err := domainNameFromSpec(spec)
	if err != nil {
		return domainSpec{}, err
	}
	if domain == "" {
		domain = spec.Name
	}
	if domain == "" || !interfaces.ValidateProviderID(domain, interfaces.IDFormatDomainName) {
		return domainSpec{}, fmt.Errorf("domain %q is not a valid domain name", domain)
	}
	accountID := strings.TrimSpace(defaultAccountID)
	if v, _ := spec.Config["account_id"].(string); strings.TrimSpace(v) != "" {
		accountID = strings.TrimSpace(v)
	}
	if accountID == "" {
		return domainSpec{}, fmt.Errorf("account_id is required")
	}
	parsed := domainSpec{Domain: domain, AccountID: accountID}
	if raw, ok := spec.Config["auto_renew"]; ok {
		value, ok := raw.(bool)
		if !ok {
			return domainSpec{}, fmt.Errorf("auto_renew must be a bool")
		}
		parsed.AutoRenew = &value
	}
	if raw, ok := spec.Config["allow_auto_renew_update"]; ok {
		value, ok := raw.(bool)
		if !ok {
			return domainSpec{}, fmt.Errorf("allow_auto_renew_update must be a bool")
		}
		parsed.AllowAutoRenewUpdate = value
	}
	return parsed, nil
}

func domainNameFromSpec(spec interfaces.ResourceSpec) (string, error) {
	if spec.Config == nil {
		return "", nil
	}
	if raw, ok := spec.Config["domain"]; ok {
		value, ok := raw.(string)
		if !ok {
			return "", fmt.Errorf("domain must be a string")
		}
		return strings.TrimSuffix(strings.TrimSpace(value), "."), nil
	}
	return "", nil
}

func domainNameFromRef(ref interfaces.ResourceRef) string {
	if ref.ProviderID != "" {
		return strings.TrimSuffix(strings.TrimSpace(ref.ProviderID), ".")
	}
	return strings.TrimSuffix(strings.TrimSpace(ref.Name), ".")
}

func domainOutput(accountID string, reg *Registration, regStatus, updateStatus *RegistrarWorkflowStatus) *interfaces.ResourceOutput {
	status := "unknown"
	domain := ""
	if reg != nil {
		status = reg.Status
		domain = reg.DomainName
	}
	outputs := map[string]any{
		"account_id":   accountID,
		"domain":       domain,
		"auto_renew":   reg != nil && reg.AutoRenew,
		"locked":       reg != nil && reg.Locked,
		"privacy_mode": "",
		"status":       status,
		"created_at":   "",
		"expires_at":   "",
	}
	if reg != nil {
		outputs["privacy_mode"] = reg.PrivacyMode
		outputs["created_at"] = formatTime(reg.CreatedAt)
		outputs["expires_at"] = formatTime(reg.ExpiresAt)
	}
	if regStatus != nil {
		outputs["registration_status"] = workflowStatusOutput(regStatus)
	}
	if updateStatus != nil {
		outputs["update_status"] = workflowStatusOutput(updateStatus)
	}
	return &interfaces.ResourceOutput{
		Name:       domain,
		Type:       domainResourceType,
		ProviderID: domain,
		Outputs:    outputs,
		Status:     status,
	}
}

func workflowStatusOutput(status *RegistrarWorkflowStatus) map[string]any {
	return map[string]any{
		"state":      status.State,
		"completed":  status.Completed,
		"created_at": formatTime(status.CreatedAt),
		"updated_at": formatTime(status.UpdatedAt),
		"error_code": status.ErrorCode,
		"error":      status.Error,
	}
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

type sdkRegistrarClient struct {
	client *cloudflare.Client
}

func newSDKRegistrarClient(apiToken string) *sdkRegistrarClient {
	return &sdkRegistrarClient{client: cloudflare.NewClient(option.WithAPIToken(apiToken))}
}

func (c *sdkRegistrarClient) GetRegistration(ctx context.Context, accountID, domain string) (*Registration, error) {
	res, err := c.client.Registrar.Registrations.Get(ctx, domain, registrar.RegistrationGetParams{
		AccountID: cloudflare.String(accountID),
	})
	if err != nil {
		return nil, err
	}
	return registrationFromSDK(res), nil
}

func (c *sdkRegistrarClient) UpdateRegistrationAutoRenew(ctx context.Context, accountID, domain string, autoRenew bool) (*RegistrarWorkflowStatus, error) {
	res, err := c.client.Registrar.Registrations.Edit(ctx, domain, registrar.RegistrationEditParams{
		AccountID: cloudflare.String(accountID),
		AutoRenew: cloudflare.Bool(autoRenew),
		Prefer:    cloudflare.F(registrar.RegistrationEditParamsPreferRespondAsync),
	})
	if err != nil {
		return nil, err
	}
	return workflowStatusFromSDK(res), nil
}

func (c *sdkRegistrarClient) GetRegistrationStatus(ctx context.Context, accountID, domain string) (*RegistrarWorkflowStatus, error) {
	res, err := c.client.Registrar.RegistrationStatus.Get(ctx, domain, registrar.RegistrationStatusGetParams{
		AccountID: cloudflare.String(accountID),
	})
	if err != nil {
		return nil, err
	}
	return workflowStatusFromSDK(res), nil
}

func (c *sdkRegistrarClient) GetUpdateStatus(ctx context.Context, accountID, domain string) (*RegistrarWorkflowStatus, error) {
	res, err := c.client.Registrar.UpdateStatus.Get(ctx, domain, registrar.UpdateStatusGetParams{
		AccountID: cloudflare.String(accountID),
	})
	if err != nil {
		return nil, err
	}
	return workflowStatusFromSDK(res), nil
}

func registrationFromSDK(reg *registrar.Registration) *Registration {
	if reg == nil {
		return nil
	}
	return &Registration{
		DomainName:  reg.DomainName,
		AutoRenew:   reg.AutoRenew,
		CreatedAt:   reg.CreatedAt,
		ExpiresAt:   reg.ExpiresAt,
		Locked:      reg.Locked,
		PrivacyMode: string(reg.PrivacyMode),
		Status:      string(reg.Status),
	}
}

func workflowStatusFromSDK(status *registrar.WorkflowStatus) *RegistrarWorkflowStatus {
	if status == nil {
		return nil
	}
	return &RegistrarWorkflowStatus{
		State:     string(status.State),
		Completed: status.Completed,
		CreatedAt: status.CreatedAt,
		UpdatedAt: status.UpdatedAt,
		ErrorCode: status.Error.Code,
		Error:     status.Error.Message,
	}
}
