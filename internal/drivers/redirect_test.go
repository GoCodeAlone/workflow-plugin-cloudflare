package drivers

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/GoCodeAlone/workflow/interfaces"
)

type fakeRedirectClient struct {
	zone    *Zone
	ruleset *RedirectRuleset

	created []*RedirectRuleset
	updated []*RedirectRuleset
}

func (f *fakeRedirectClient) GetZone(_ context.Context, domain, zoneID string) (*Zone, error) {
	if f.zone != nil {
		return f.zone, nil
	}
	if zoneID != "" {
		return &Zone{ID: zoneID, Name: domain, Status: "active"}, nil
	}
	return nil, errors.New("zone not found")
}

func (f *fakeRedirectClient) GetRedirectRuleset(_ context.Context, zoneID string) (*RedirectRuleset, error) {
	if f.ruleset == nil {
		return nil, interfaces.ErrResourceNotFound
	}
	copy := *f.ruleset
	copy.ZoneID = zoneID
	copy.Rules = append([]RedirectRule(nil), f.ruleset.Rules...)
	return &copy, nil
}

func (f *fakeRedirectClient) CreateRedirectRuleset(_ context.Context, zoneID string, rules []RedirectRule) (*RedirectRuleset, error) {
	out := &RedirectRuleset{ID: "ruleset-created", ZoneID: zoneID, Name: redirectRulesetName, Rules: append([]RedirectRule(nil), rules...)}
	f.ruleset = out
	f.created = append(f.created, out)
	return out, nil
}

func (f *fakeRedirectClient) UpdateRedirectRuleset(_ context.Context, zoneID, rulesetID string, rules []RedirectRule) (*RedirectRuleset, error) {
	out := &RedirectRuleset{ID: rulesetID, ZoneID: zoneID, Name: redirectRulesetName, Rules: append([]RedirectRule(nil), rules...)}
	f.ruleset = out
	f.updated = append(f.updated, out)
	return out, nil
}

func TestRedirectDriverCreateCreatesPhaseRuleset(t *testing.T) {
	fake := &fakeRedirectClient{zone: &Zone{ID: "zone", Name: "example.net", Status: "active"}}
	driver := NewRedirectDriverWithClient(fake)

	out, err := driver.Create(context.Background(), interfaces.ResourceSpec{
		Name: "redirect-example-net",
		Type: redirectResourceType,
		Config: map[string]any{
			"domain":      "example.net",
			"from_host":   "example.net",
			"target_url":  "https://example.com",
			"status_code": 301,
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(fake.created) != 1 {
		t.Fatalf("created rulesets = %d, want 1", len(fake.created))
	}
	rule := fake.created[0].Rules[0]
	if rule.Ref != "workflow_redirect_example_net" {
		t.Fatalf("rule ref = %q", rule.Ref)
	}
	if rule.Expression != `(http.host eq "example.net")` {
		t.Fatalf("rule expression = %q", rule.Expression)
	}
	if rule.TargetURL != "https://example.com" || rule.StatusCode != 301 || !rule.PreservePath || !rule.PreserveQueryString {
		t.Fatalf("rule = %#v, want target/status/preserve defaults", rule)
	}
	if out.ProviderID != "zone/workflow_redirect_example_net" {
		t.Fatalf("ProviderID = %q, want zone/ref", out.ProviderID)
	}
}

func TestRedirectDriverUpdatePreservesUnrelatedRulesAndReplacesManagedRef(t *testing.T) {
	fake := &fakeRedirectClient{
		zone: &Zone{ID: "zone", Name: "example.net", Status: "active"},
		ruleset: &RedirectRuleset{
			ID:     "ruleset",
			ZoneID: "zone",
			Rules: []RedirectRule{
				{Ref: "keep", Expression: `(http.host eq "keep.example.net")`, TargetURL: "https://keep.example.com", StatusCode: 302},
				{Ref: "workflow_redirect_example_net", Expression: `(http.host eq "example.net")`, TargetURL: "https://old.example.com", StatusCode: 302},
			},
		},
	}
	driver := NewRedirectDriverWithClient(fake)

	_, err := driver.Update(context.Background(),
		interfaces.ResourceRef{Name: "redirect-example-net", Type: redirectResourceType, ProviderID: "zone/workflow_redirect_example_net"},
		interfaces.ResourceSpec{
			Name: "redirect-example-net",
			Type: redirectResourceType,
			Config: map[string]any{
				"domain":      "example.net",
				"from_host":   "example.net",
				"target_url":  "https://new.example.com",
				"status_code": 308,
			},
		},
	)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(fake.updated) != 1 {
		t.Fatalf("updated rulesets = %d, want 1", len(fake.updated))
	}
	if len(fake.updated[0].Rules) != 2 {
		t.Fatalf("rules = %#v, want unrelated + managed", fake.updated[0].Rules)
	}
	if fake.updated[0].Rules[0].Ref != "keep" {
		t.Fatalf("first rule = %#v, want unrelated rule preserved in order", fake.updated[0].Rules[0])
	}
	if got := fake.updated[0].Rules[1].TargetURL; got != "https://new.example.com" {
		t.Fatalf("managed target = %q", got)
	}
}

func TestRedirectDriverDiffMatchesCurrentOutput(t *testing.T) {
	driver := NewRedirectDriverWithClient(&fakeRedirectClient{})
	diff, err := driver.Diff(context.Background(),
		interfaces.ResourceSpec{
			Name: "redirect-example-net",
			Type: redirectResourceType,
			Config: map[string]any{
				"domain":      "example.net",
				"from_host":   "example.net",
				"target_url":  "https://example.com",
				"status_code": 301,
			},
		},
		&interfaces.ResourceOutput{
			Name:       "redirect-example-net",
			Type:       redirectResourceType,
			ProviderID: "zone/workflow_redirect_example_net",
			Outputs: map[string]any{
				"domain":                "example.net",
				"from_host":             "example.net",
				"target_url":            "https://example.com",
				"status_code":           301,
				"preserve_path":         true,
				"preserve_query_string": true,
				"enabled":               true,
			},
		},
	)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if diff.NeedsUpdate {
		t.Fatalf("diff = %#v, want no update", diff)
	}
}

func TestRedirectDriverRejectsInvalidTargetURL(t *testing.T) {
	driver := NewRedirectDriverWithClient(&fakeRedirectClient{})
	_, err := driver.Create(context.Background(), interfaces.ResourceSpec{
		Name: "redirect-example-net",
		Type: redirectResourceType,
		Config: map[string]any{
			"domain":     "example.net",
			"target_url": "example.com",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "target_url") {
		t.Fatalf("expected target_url validation error, got %v", err)
	}
}
