package drivers

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/GoCodeAlone/workflow/interfaces"
)

type fakeRegistrarClient struct {
	registration *Registration
	status       *RegistrarWorkflowStatus
	updateStatus *RegistrarWorkflowStatus
	autoRenew    []bool
}

func (f *fakeRegistrarClient) GetRegistration(_ context.Context, _, domain string) (*Registration, error) {
	if f.registration == nil {
		return nil, errors.New("registration not found")
	}
	out := *f.registration
	if out.DomainName == "" {
		out.DomainName = domain
	}
	return &out, nil
}

func (f *fakeRegistrarClient) UpdateRegistrationAutoRenew(_ context.Context, _, _ string, autoRenew bool) (*RegistrarWorkflowStatus, error) {
	f.autoRenew = append(f.autoRenew, autoRenew)
	return &RegistrarWorkflowStatus{State: "pending", Completed: false}, nil
}

func (f *fakeRegistrarClient) GetRegistrationStatus(_ context.Context, _, _ string) (*RegistrarWorkflowStatus, error) {
	return f.status, nil
}

func (f *fakeRegistrarClient) GetUpdateStatus(_ context.Context, _, _ string) (*RegistrarWorkflowStatus, error) {
	return f.updateStatus, nil
}

func TestDomainDriver_ReadPreservesRegistrarMetadata(t *testing.T) {
	created := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	expires := time.Date(2027, 6, 7, 8, 9, 10, 0, time.UTC)
	updated := time.Date(2026, 5, 24, 1, 2, 3, 0, time.UTC)
	driver := NewDomainDriverWithClient("acct", &fakeRegistrarClient{
		registration: &Registration{
			DomainName:  "example.com",
			AutoRenew:   true,
			CreatedAt:   created,
			ExpiresAt:   expires,
			Locked:      true,
			PrivacyMode: "redaction",
			Status:      "active",
		},
		status:       &RegistrarWorkflowStatus{State: "succeeded", Completed: true, UpdatedAt: updated},
		updateStatus: &RegistrarWorkflowStatus{State: "succeeded", Completed: true},
	})
	out, err := driver.Read(context.Background(), interfaces.ResourceRef{Name: "example.com", Type: "infra.domain"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if out.ProviderID != "example.com" {
		t.Fatalf("ProviderID = %q, want example.com", out.ProviderID)
	}
	if out.Outputs["account_id"] != "acct" || out.Outputs["auto_renew"] != true || out.Outputs["locked"] != true {
		t.Fatalf("outputs = %#v, want account_id/auto_renew/locked", out.Outputs)
	}
	if out.Outputs["registration_status"].(map[string]any)["state"] != "succeeded" {
		t.Fatalf("registration_status = %#v", out.Outputs["registration_status"])
	}
	if out.Outputs["created_at"] != created.Format(time.RFC3339) || out.Outputs["expires_at"] != expires.Format(time.RFC3339) {
		t.Fatalf("timestamps = %#v", out.Outputs)
	}
}

func TestDomainDriver_CreateAndDeleteRefusePurchasingOrDeletion(t *testing.T) {
	driver := NewDomainDriverWithClient("acct", &fakeRegistrarClient{})
	_, err := driver.Create(context.Background(), interfaces.ResourceSpec{Name: "example.com", Type: "infra.domain"})
	if err == nil || !strings.Contains(err.Error(), "import-first") {
		t.Fatalf("Create err = %v, want import-first refusal", err)
	}
	err = driver.Delete(context.Background(), interfaces.ResourceRef{Name: "example.com", Type: "infra.domain"})
	if err == nil || !strings.Contains(err.Error(), "refuses to delete") {
		t.Fatalf("Delete err = %v, want delete refusal", err)
	}
}

func TestDomainDriver_DiffOnlyTracksDeclaredAutoRenew(t *testing.T) {
	driver := NewDomainDriverWithClient("acct", &fakeRegistrarClient{})
	current := &interfaces.ResourceOutput{
		Name:       "example.com",
		Type:       "infra.domain",
		ProviderID: "example.com",
		Outputs: map[string]any{
			"domain":     "example.com",
			"auto_renew": false,
			"locked":     true,
		},
	}
	diff, err := driver.Diff(context.Background(), interfaces.ResourceSpec{
		Name:   "example.com",
		Type:   "infra.domain",
		Config: map[string]any{"domain": "example.com"},
	}, current)
	if err != nil {
		t.Fatalf("Diff omitted auto_renew: %v", err)
	}
	if diff.NeedsUpdate {
		t.Fatalf("diff = %#v, want no update when auto_renew omitted", diff)
	}
	diff, err = driver.Diff(context.Background(), interfaces.ResourceSpec{
		Name: "example.com",
		Type: "infra.domain",
		Config: map[string]any{
			"domain":     "example.com",
			"auto_renew": true,
		},
	}, current)
	if err != nil {
		t.Fatalf("Diff declared auto_renew: %v", err)
	}
	if !diff.NeedsUpdate || len(diff.Changes) != 1 || diff.Changes[0].Path != "auto_renew" {
		t.Fatalf("diff = %#v, want auto_renew update", diff)
	}
}

func TestDomainDriver_DiffRequiresExistingImportedState(t *testing.T) {
	driver := NewDomainDriverWithClient("acct", &fakeRegistrarClient{})
	_, err := driver.Diff(context.Background(), interfaces.ResourceSpec{
		Name: "example.com",
		Type: "infra.domain",
		Config: map[string]any{
			"domain": "example.com",
		},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "import-first") {
		t.Fatalf("Diff err = %v, want import-first refusal", err)
	}
}

func TestDomainDriver_DiffTreatsAccountChangeAsReplace(t *testing.T) {
	driver := NewDomainDriverWithClient("acct-new", &fakeRegistrarClient{})
	diff, err := driver.Diff(context.Background(), interfaces.ResourceSpec{
		Name: "example.com",
		Type: "infra.domain",
		Config: map[string]any{
			"domain": "example.com",
		},
	}, &interfaces.ResourceOutput{
		Name:       "example.com",
		Type:       "infra.domain",
		ProviderID: "example.com",
		Outputs: map[string]any{
			"domain":     "example.com",
			"account_id": "acct-old",
		},
	})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !diff.NeedsReplace || len(diff.Changes) != 1 || diff.Changes[0].Path != "account_id" {
		t.Fatalf("diff = %#v, want account_id replacement", diff)
	}
}

func TestDomainDriver_UpdateAutoRenewRequiresExplicitOptIn(t *testing.T) {
	fake := &fakeRegistrarClient{registration: &Registration{DomainName: "example.com", AutoRenew: false, Status: "active"}}
	driver := NewDomainDriverWithClient("acct", fake)
	spec := interfaces.ResourceSpec{
		Name: "example.com",
		Type: "infra.domain",
		Config: map[string]any{
			"domain":     "example.com",
			"auto_renew": true,
		},
	}
	_, err := driver.Update(context.Background(), interfaces.ResourceRef{Name: "example.com", Type: "infra.domain"}, spec)
	if err == nil || !strings.Contains(err.Error(), "allow_auto_renew_update") {
		t.Fatalf("Update err = %v, want opt-in refusal", err)
	}
	if len(fake.autoRenew) != 0 {
		t.Fatalf("autoRenew calls = %#v, want none", fake.autoRenew)
	}
	spec.Config["allow_auto_renew_update"] = true
	_, err = driver.Update(context.Background(), interfaces.ResourceRef{Name: "example.com", Type: "infra.domain"}, spec)
	if err != nil {
		t.Fatalf("Update with opt-in: %v", err)
	}
	if len(fake.autoRenew) != 1 || fake.autoRenew[0] != true {
		t.Fatalf("autoRenew calls = %#v, want true", fake.autoRenew)
	}
}
