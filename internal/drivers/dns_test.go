package drivers

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/GoCodeAlone/workflow/interfaces"
)

type fakeCFClient struct {
	zone            *Zone
	records         []Record
	dnssec          *DNSSEC
	lastGetDomain   string
	lastGetZoneID   string
	createdZones    []string
	createdAccounts []string
	createdRecords  []Record
	updatedRecords  []Record
	deletedRecords  []string
}

func (f *fakeCFClient) GetZone(_ context.Context, domain, zoneID string) (*Zone, error) {
	f.lastGetDomain = domain
	f.lastGetZoneID = zoneID
	if f.zone != nil {
		return f.zone, nil
	}
	if zoneID != "" {
		return &Zone{ID: zoneID, Name: domain, Status: "active"}, nil
	}
	return nil, errors.New("zone not found")
}

func (f *fakeCFClient) CreateZone(_ context.Context, accountID string, domain string) (*Zone, error) {
	f.createdAccounts = append(f.createdAccounts, accountID)
	f.createdZones = append(f.createdZones, domain)
	f.zone = &Zone{ID: "zone-created", Name: domain, Status: "pending", NameServers: []string{"a.ns.cloudflare.com", "b.ns.cloudflare.com"}}
	return f.zone, nil
}

func TestDNSDriver_CreateUsesDefaultAccountID(t *testing.T) {
	fake := &fakeCFClient{}
	driver := NewDNSDriverWithClientAndAccount(fake, "acct-default")
	_, err := driver.Create(context.Background(), interfaces.ResourceSpec{
		Name: "example.com",
		Type: "infra.dns",
		Config: map[string]any{
			"domain":  "example.com",
			"records": []any{},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(fake.createdAccounts) != 1 || fake.createdAccounts[0] != "acct-default" {
		t.Fatalf("createdAccounts = %#v, want acct-default", fake.createdAccounts)
	}
}

func TestDNSDriver_CreateResourceAccountIDOverridesDefault(t *testing.T) {
	fake := &fakeCFClient{}
	driver := NewDNSDriverWithClientAndAccount(fake, "acct-default")
	_, err := driver.Create(context.Background(), interfaces.ResourceSpec{
		Name: "example.com",
		Type: "infra.dns",
		Config: map[string]any{
			"domain":     "example.com",
			"account_id": "acct-resource",
			"records":    []any{},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(fake.createdAccounts) != 1 || fake.createdAccounts[0] != "acct-resource" {
		t.Fatalf("createdAccounts = %#v, want acct-resource", fake.createdAccounts)
	}
}

func (f *fakeCFClient) DeleteZone(_ context.Context, zoneID string) error { return nil }

func (f *fakeCFClient) ListRecords(_ context.Context, _ string) ([]Record, error) {
	return slices.Clone(f.records), nil
}

func (f *fakeCFClient) CreateRecord(_ context.Context, _ string, record Record) (*Record, error) {
	record.ID = "created"
	f.createdRecords = append(f.createdRecords, record)
	f.records = append(f.records, record)
	return &record, nil
}

func (f *fakeCFClient) UpdateRecord(_ context.Context, _ string, recordID string, record Record) (*Record, error) {
	record.ID = recordID
	f.updatedRecords = append(f.updatedRecords, record)
	for i := range f.records {
		if f.records[i].ID == recordID {
			f.records[i] = record
		}
	}
	return &record, nil
}

func (f *fakeCFClient) DeleteRecord(_ context.Context, _ string, recordID string) error {
	f.deletedRecords = append(f.deletedRecords, recordID)
	return nil
}

func (f *fakeCFClient) GetDNSSEC(_ context.Context, _ string) (*DNSSEC, error) {
	return f.dnssec, nil
}

func TestDNSDriver_CreateCreatesMissingZoneAndRecord(t *testing.T) {
	proxied := true
	fake := &fakeCFClient{dnssec: &DNSSEC{Status: "active", DS: "12345 13 2 abc"}}
	driver := NewDNSDriverWithClient(fake)
	out, err := driver.Create(context.Background(), interfaces.ResourceSpec{
		Name: "example.com",
		Type: "infra.dns",
		Config: map[string]any{
			"domain":     "example.com",
			"account_id": "acct",
			"records": []any{
				map[string]any{"type": "A", "name": "@", "data": "203.0.113.10", "ttl": 300, "proxied": true},
			},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(fake.createdZones) != 1 || fake.createdZones[0] != "example.com" {
		t.Fatalf("createdZones = %#v, want example.com", fake.createdZones)
	}
	if len(fake.createdRecords) != 1 {
		t.Fatalf("createdRecords len = %d, want 1", len(fake.createdRecords))
	}
	if fake.createdRecords[0].Proxied == nil || *fake.createdRecords[0].Proxied != proxied {
		t.Fatalf("proxied = %#v, want true", fake.createdRecords[0].Proxied)
	}
	if out.ProviderID != "zone-created" {
		t.Fatalf("ProviderID = %q, want zone-created", out.ProviderID)
	}
	if out.Outputs["dnssec"].(map[string]any)["status"] != "active" {
		t.Fatalf("dnssec output = %#v", out.Outputs["dnssec"])
	}
}

func TestDNSDriver_CreateTimesOutBlockedClientOperation(t *testing.T) {
	fake := &blockingListRecordsClient{
		fakeCFClient: fakeCFClient{
			zone: &Zone{ID: "zone", Name: "example.com", Status: "active"},
		},
	}
	driver := NewDNSDriverWithClientAndAccountTimeout(fake, "acct", 10*time.Millisecond)
	start := time.Now()
	_, err := driver.Create(context.Background(), interfaces.ResourceSpec{
		Name: "example.com",
		Type: "infra.dns",
		Config: map[string]any{
			"domain":  "example.com",
			"records": []any{},
		},
	})
	if err == nil {
		t.Fatal("Create returned nil error, want context deadline")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Create error = %v, want context deadline", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Create took %s, want bounded timeout", elapsed)
	}
}

type blockingListRecordsClient struct {
	fakeCFClient
}

func (f *blockingListRecordsClient) ListRecords(ctx context.Context, _ string) ([]Record, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestDNSDriver_ReadIncludesZoneMetadataAndRecords(t *testing.T) {
	fake := &fakeCFClient{
		zone: &Zone{
			ID:                  "zone",
			Name:                "example.com",
			Status:              "active",
			NameServers:         []string{"ada.ns.cloudflare.com", "bob.ns.cloudflare.com"},
			OriginalNameServers: []string{"ns1.hover.com"},
			OriginalRegistrar:   "Hover",
			OriginalDNSHost:     "DigitalOcean",
		},
		records: []Record{{ID: "rec", Type: "MX", Name: "example.com", Data: "aspmx.l.google.com", TTL: 300, Priority: 1}},
	}
	driver := NewDNSDriverWithClient(fake)
	out, err := driver.Read(context.Background(), interfaces.ResourceRef{Name: "example.com", Type: "infra.dns", ProviderID: "zone"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if out.Outputs["original_registrar"] != "Hover" {
		t.Fatalf("original_registrar = %#v", out.Outputs["original_registrar"])
	}
	authority, ok := out.Outputs["authority"].(map[string]any)
	if !ok {
		t.Fatalf("authority = %T, want map[string]any", out.Outputs["authority"])
	}
	if got := authority["role"]; got != "target_authoritative_dns" {
		t.Fatalf("authority.role = %v, want target_authoritative_dns", got)
	}
	if got := authority["dns_host"]; got != "Cloudflare" {
		t.Fatalf("authority.dns_host = %v, want Cloudflare", got)
	}
	nameservers, ok := authority["name_servers"].([]string)
	if !ok || len(nameservers) != 2 || nameservers[0] != "ada.ns.cloudflare.com" {
		t.Fatalf("authority.name_servers = %#v, want Cloudflare nameservers", authority["name_servers"])
	}
	original, ok := authority["original_name_servers"].([]string)
	if !ok || len(original) != 1 || original[0] != "ns1.hover.com" {
		t.Fatalf("authority.original_name_servers = %#v, want original nameservers", authority["original_name_servers"])
	}
	if got := authority["original_registrar"]; got != "Hover" {
		t.Fatalf("authority.original_registrar = %v, want Hover", got)
	}
	if got := authority["original_dnshost"]; got != "DigitalOcean" {
		t.Fatalf("authority.original_dnshost = %v, want DigitalOcean", got)
	}
	records := out.Outputs["records"].([]map[string]any)
	if len(records) != 1 || records[0]["priority"] != 1 {
		t.Fatalf("records = %#v, want MX priority", records)
	}
}

func TestDNSDriver_ReadTreatsDomainProviderIDAsDomainNotZoneID(t *testing.T) {
	fake := &fakeCFClient{zone: &Zone{ID: "zone", Name: "example.com"}}
	driver := NewDNSDriverWithClient(fake)
	_, err := driver.Read(context.Background(), interfaces.ResourceRef{Name: "example.com", Type: "infra.dns", ProviderID: "example.com"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if fake.lastGetDomain != "example.com" || fake.lastGetZoneID != "" {
		t.Fatalf("GetZone(domain, zoneID) = (%q, %q), want (example.com, empty)", fake.lastGetDomain, fake.lastGetZoneID)
	}
}

func TestDNSDriver_ReadMissingZoneReturnsResourceNotFound(t *testing.T) {
	driver := NewDNSDriverWithClient(&fakeCFClient{})
	_, err := driver.Read(context.Background(), interfaces.ResourceRef{Name: "example.com", Type: "infra.dns", ProviderID: "example.com"})
	if err == nil {
		t.Fatal("Read missing zone: expected error, got nil")
	}
	if !errors.Is(err, interfaces.ErrResourceNotFound) {
		t.Fatalf("Read missing zone error = %v, want ErrResourceNotFound", err)
	}
}

func TestDNSDriver_AdoptionRefUsesDomainProviderID(t *testing.T) {
	driver := NewDNSDriverWithClientAndAccount(&fakeCFClient{}, "acct")
	ref, ok, err := driver.AdoptionRef(interfaces.ResourceSpec{
		Name: "cf-example-com",
		Type: "infra.dns",
		Config: map[string]any{
			"domain":  "example.com",
			"records": []any{},
		},
	})
	if err != nil {
		t.Fatalf("AdoptionRef: %v", err)
	}
	if !ok {
		t.Fatal("AdoptionRef ok = false, want true")
	}
	if ref.Name != "example.com" || ref.ProviderID != "example.com" || ref.Type != "infra.dns" {
		t.Fatalf("AdoptionRef = %#v, want domain name/provider id", ref)
	}
}

func TestDNSDriver_DiffDetectsProxiedTTLAndPriority(t *testing.T) {
	driver := NewDNSDriverWithClient(&fakeCFClient{})
	current := &interfaces.ResourceOutput{
		Name:       "example.com",
		Type:       "infra.dns",
		ProviderID: "zone",
		Outputs: map[string]any{
			"domain": "example.com",
			"records": []map[string]any{{
				"type": "MX", "name": "example.com", "data": "aspmx.l.google.com", "ttl": 300, "priority": 1, "proxied": false,
			}},
		},
	}
	diff, err := driver.Diff(context.Background(), interfaces.ResourceSpec{
		Name: "example.com",
		Type: "infra.dns",
		Config: map[string]any{
			"domain": "example.com",
			"records": []any{
				map[string]any{"type": "MX", "name": "@", "data": "aspmx.l.google.com", "ttl": 600, "priority": 5, "proxied": false},
			},
		},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !diff.NeedsUpdate || len(diff.Changes) == 0 {
		t.Fatalf("diff = %#v, want update", diff)
	}
}

func TestDNSDriver_DiffDoesNotManageProxiedWhenOmitted(t *testing.T) {
	driver := NewDNSDriverWithClient(&fakeCFClient{})
	current := &interfaces.ResourceOutput{
		Name:       "example.com",
		Type:       "infra.dns",
		ProviderID: "zone",
		Outputs: map[string]any{
			"domain": "example.com",
			"records": []map[string]any{{
				"type": "A", "name": "example.com", "data": "203.0.113.10", "ttl": 300, "proxied": true,
			}},
		},
	}
	diff, err := driver.Diff(context.Background(), interfaces.ResourceSpec{
		Name: "example.com",
		Type: "infra.dns",
		Config: map[string]any{
			"domain": "example.com",
			"records": []any{
				map[string]any{"type": "A", "name": "@", "data": "203.0.113.10", "ttl": 300},
			},
		},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if diff.NeedsUpdate {
		t.Fatalf("diff = %#v, want no update when proxied is omitted", diff)
	}
}

func TestDNSDriver_UpdatePreservesUnlistedRecordsByDefault(t *testing.T) {
	fake := &fakeCFClient{
		zone: &Zone{ID: "zone", Name: "example.com"},
		records: []Record{
			{ID: "keep", Type: "TXT", Name: "example.com", Data: "v=spf1 include:_spf.google.com ~all", TTL: 300},
			{ID: "a", Type: "A", Name: "example.com", Data: "203.0.113.10", TTL: 300},
		},
	}
	driver := NewDNSDriverWithClient(fake)
	_, err := driver.Update(context.Background(), interfaces.ResourceRef{Name: "example.com", Type: "infra.dns", ProviderID: "zone"}, interfaces.ResourceSpec{
		Name: "example.com",
		Type: "infra.dns",
		Config: map[string]any{
			"domain": "example.com",
			"records": []any{
				map[string]any{"type": "A", "name": "@", "data": "203.0.113.10", "ttl": 300},
			},
		},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(fake.deletedRecords) != 0 {
		t.Fatalf("deletedRecords = %#v, want none", fake.deletedRecords)
	}
}

func TestDNSDriver_UpdateDeletesUnlistedRecordsWhenManaged(t *testing.T) {
	fake := &fakeCFClient{
		zone: &Zone{ID: "zone", Name: "example.com"},
		records: []Record{
			{ID: "delete-me", Type: "TXT", Name: "example.com", Data: "stale", TTL: 300},
		},
	}
	driver := NewDNSDriverWithClient(fake)
	_, err := driver.Update(context.Background(), interfaces.ResourceRef{Name: "example.com", Type: "infra.dns", ProviderID: "zone"}, interfaces.ResourceSpec{
		Name: "example.com",
		Type: "infra.dns",
		Config: map[string]any{
			"domain":          "example.com",
			"manage_unlisted": true,
			"records":         []any{},
		},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(fake.deletedRecords) != 1 || fake.deletedRecords[0] != "delete-me" {
		t.Fatalf("deletedRecords = %#v, want delete-me", fake.deletedRecords)
	}
}

func TestDNSDriver_MissingRecordsErrorsBeforeMutation(t *testing.T) {
	fake := &fakeCFClient{zone: &Zone{ID: "zone", Name: "example.com"}}
	driver := NewDNSDriverWithClient(fake)
	_, err := driver.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "example.com",
		Type:   "infra.dns",
		Config: map[string]any{"domain": "example.com"},
	})
	if err == nil {
		t.Fatal("expected missing records error")
	}
	if len(fake.createdRecords) != 0 || len(fake.createdZones) != 0 {
		t.Fatalf("mutated before validation: zones=%#v records=%#v", fake.createdZones, fake.createdRecords)
	}
}
