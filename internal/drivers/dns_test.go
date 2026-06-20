package drivers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/cloudflare/cloudflare-go/v7/option"
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
	operations      []string
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
	f.operations = append(f.operations, "create:"+record.Type+":"+record.Name)
	f.createdRecords = append(f.createdRecords, record)
	f.records = append(f.records, record)
	return &record, nil
}

func (f *fakeCFClient) UpdateRecord(_ context.Context, _ string, recordID string, record Record) (*Record, error) {
	record.ID = recordID
	f.operations = append(f.operations, "update:"+recordID+":"+record.Type+":"+record.Name)
	f.updatedRecords = append(f.updatedRecords, record)
	for i := range f.records {
		if f.records[i].ID == recordID {
			f.records[i] = record
		}
	}
	return &record, nil
}

func (f *fakeCFClient) DeleteRecord(_ context.Context, _ string, recordID string) error {
	f.operations = append(f.operations, "delete:"+recordID)
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

func TestDNSDriver_CreateCanonicalizesTXTContentInState(t *testing.T) {
	fake := &fakeCFClient{}
	driver := NewDNSDriverWithClient(fake)
	_, err := driver.Create(context.Background(), interfaces.ResourceSpec{
		Name: "example.com",
		Type: "infra.dns",
		Config: map[string]any{
			"domain":     "example.com",
			"account_id": "acct",
			"records": []any{
				map[string]any{"type": "TXT", "name": "@", "data": "google-site-verification=abc123", "ttl": 300},
				map[string]any{"type": "TXT", "name": "_dmarc", "data": `"v=DMARC1; p=none"`, "ttl": 300},
			},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(fake.createdRecords) != 2 {
		t.Fatalf("createdRecords len = %d, want 2", len(fake.createdRecords))
	}
	if got, want := fake.createdRecords[0].Data, "google-site-verification=abc123"; got != want {
		t.Fatalf("TXT data sent to Cloudflare = %q, want %q", got, want)
	}
	if got, want := fake.createdRecords[1].Data, "v=DMARC1; p=none"; got != want {
		t.Fatalf("quoted TXT input sent to Cloudflare = %q, want raw %q", got, want)
	}
}

func TestDNSDriver_RecordBodiesSendQuotedTXTContentToCloudflare(t *testing.T) {
	newBody := newRecordBody(Record{Type: "TXT", Name: "example.com", Data: "google-site-verification=abc123", TTL: 300})
	if got, want := newBody.Content.Value, `"google-site-verification=abc123"`; got != want {
		t.Fatalf("new TXT content = %q, want %q", got, want)
	}

	editBody := editRecordBody(Record{Type: "TXT", Name: "_dmarc.example.com", Data: `"v=DMARC1; p=none"`, TTL: 300})
	if got, want := editBody.Content.Value, `"v=DMARC1; p=none"`; got != want {
		t.Fatalf("edit TXT content = %q, want %q", got, want)
	}

	escapedBody := newRecordBody(Record{Type: "TXT", Name: "escaped.example.com", Data: `owner="wfctl"\state`, TTL: 300})
	if got, want := escapedBody.Content.Value, `"owner=\"wfctl\"\\state"`; got != want {
		t.Fatalf("escaped TXT content = %q, want %q", got, want)
	}
	if got, want := rawTXTData("TXT", escapedBody.Content.Value), `owner="wfctl"\state`; got != want {
		t.Fatalf("escaped TXT canonical data = %q, want %q", got, want)
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

func TestDNSDriver_RequestTimeoutDoesNotShrinkOperationTimeout(t *testing.T) {
	driver := NewDNSDriverWithAccountRequestTimeout("token", "acct", 10*time.Millisecond)
	if driver.operationTimeout != defaultOperationTimeout {
		t.Fatalf("operationTimeout = %s, want %s", driver.operationTimeout, defaultOperationTimeout)
	}
}

func TestSDKClientCreateZoneDoesNotRetryConflict(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/zones" {
			t.Errorf("request = %s %s, want POST /zones", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = fmt.Fprint(w, `{"success":false,"errors":[{"code":1099,"message":"conflict"}],"messages":[],"result":null}`)
	}))
	t.Cleanup(server.Close)

	client := newSDKClientWithOptions("token", time.Second, option.WithBaseURL(server.URL))
	_, err := client.CreateZone(context.Background(), "acct", "example.com")
	if err == nil {
		t.Fatal("CreateZone returned nil error, want conflict")
	}
	if strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
		t.Fatalf("CreateZone error = %v, want API conflict without context deadline", err)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("POST /zones hits = %d, want 1", got)
	}
}

func TestSDKClientListRecordsUsesConservativePageSizeAndStopsAtTotalPages(t *testing.T) {
	var page1Hits atomic.Int32
	var page2Hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/zones/zone123/dns_records" {
			t.Errorf("request = %s %s, want GET /zones/zone123/dns_records", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("per_page"); got != "100" {
			t.Errorf("per_page = %q, want 100", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("page") {
		case "1":
			page1Hits.Add(1)
			_, _ = fmt.Fprint(w, `{"success":true,"errors":[],"messages":[],"result":[{"id":"rec1","type":"A","name":"example.com","content":"192.0.2.10","ttl":300}],"result_info":{"page":1,"per_page":100,"count":1,"total_count":1,"total_pages":1}}`)
		case "2":
			page2Hits.Add(1)
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, `{"success":false,"errors":[{"code":1000,"message":"unexpected page 2"}],"messages":[],"result":null}`)
		default:
			t.Errorf("unexpected page query %q", r.URL.Query().Get("page"))
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)

	client := newSDKClientWithOptions("token", time.Second, option.WithBaseURL(server.URL))
	records, err := client.ListRecords(context.Background(), "zone123")
	if err != nil {
		t.Fatalf("ListRecords: %v", err)
	}
	if len(records) != 1 || records[0].ID != "rec1" {
		t.Fatalf("records = %#v, want rec1", records)
	}
	if got := page1Hits.Load(); got != 1 {
		t.Fatalf("page 1 hits = %d, want 1", got)
	}
	if got := page2Hits.Load(); got != 0 {
		t.Fatalf("page 2 hits = %d, want 0 because total_pages=1", got)
	}
}

func TestSDKClientListRecordsRetriesPageFailures(t *testing.T) {
	var page1Hits atomic.Int32
	var page2Hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/zones/zone123/dns_records" {
			t.Errorf("request = %s %s, want GET /zones/zone123/dns_records", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("per_page"); got != "100" {
			t.Errorf("per_page = %q, want 100", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("page") {
		case "1":
			page1Hits.Add(1)
			_, _ = fmt.Fprint(w, `{"success":true,"errors":[],"messages":[],"result":[{"id":"rec1","type":"A","name":"example.com","content":"192.0.2.10","ttl":300}],"result_info":{"page":1,"per_page":100,"count":1,"total_count":1,"total_pages":2}}`)
		case "2":
			hit := page2Hits.Add(1)
			if hit == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = fmt.Fprint(w, `{"success":false,"errors":[{"code":10001,"message":"Unable to authenticate request"}],"messages":[],"result":null}`)
				return
			}
			_, _ = fmt.Fprint(w, `{"success":true,"errors":[],"messages":[],"result":[],"result_info":{"page":2,"per_page":100,"count":0,"total_count":1,"total_pages":2}}`)
		default:
			t.Errorf("unexpected page query %q", r.URL.Query().Get("page"))
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)

	client := newSDKClientWithOptions("token", time.Second, option.WithBaseURL(server.URL))
	records, err := client.ListRecords(context.Background(), "zone123")
	if err != nil {
		t.Fatalf("ListRecords: %v", err)
	}
	if len(records) != 1 || records[0].ID != "rec1" {
		t.Fatalf("records = %#v, want rec1", records)
	}
	if got := page1Hits.Load(); got != 1 {
		t.Fatalf("page 1 hits = %d, want 1", got)
	}
	if got := page2Hits.Load(); got != 2 {
		t.Fatalf("page 2 hits = %d, want retry after transient failure", got)
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

func TestDNSDriver_DiffDoesNotManageComputedRecordFieldsWhenOmitted(t *testing.T) {
	driver := NewDNSDriverWithClient(&fakeCFClient{})
	current := &interfaces.ResourceOutput{
		Name:       "example.com",
		Type:       "infra.dns",
		ProviderID: "zone",
		Outputs: map[string]any{
			"domain": "example.com",
			"records": []map[string]any{{
				"id": "placeholder", "type": "A", "name": "example.com", "data": "192.0.2.1", "ttl": 1,
				"comment": "Originless placeholder for Cloudflare redirect rules", "proxiable": true, "proxied": true,
			}},
		},
	}
	diff, err := driver.Diff(context.Background(), interfaces.ResourceSpec{
		Name: "example.com",
		Type: "infra.dns",
		Config: map[string]any{
			"domain": "example.com",
			"records": []any{
				map[string]any{"type": "A", "name": "@", "data": "192.0.2.1", "ttl": 1},
			},
		},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if diff.NeedsUpdate {
		t.Fatalf("diff = %#v, want no update when computed fields are omitted", diff)
	}
}

func TestDNSDriver_DiffManagesExplicitNonEmptyComment(t *testing.T) {
	driver := NewDNSDriverWithClient(&fakeCFClient{})
	current := &interfaces.ResourceOutput{
		Name:       "example.com",
		Type:       "infra.dns",
		ProviderID: "zone",
		Outputs: map[string]any{
			"domain": "example.com",
			"records": []map[string]any{{
				"type": "A", "name": "example.com", "data": "192.0.2.1", "ttl": 1,
				"comment": "provider comment",
			}},
		},
	}
	diff, err := driver.Diff(context.Background(), interfaces.ResourceSpec{
		Name: "example.com",
		Type: "infra.dns",
		Config: map[string]any{
			"domain": "example.com",
			"records": []any{
				map[string]any{"type": "A", "name": "@", "data": "192.0.2.1", "ttl": 1, "comment": "managed comment"},
			},
		},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !diff.NeedsUpdate {
		t.Fatalf("diff = %#v, want update when explicit non-empty comment differs", diff)
	}
}

func TestDNSDriver_UpdatePreservesComputedRecordFieldsWhenOmitted(t *testing.T) {
	proxied := true
	fake := &fakeCFClient{
		zone: &Zone{ID: "zone", Name: "example.com"},
		records: []Record{{
			ID: "placeholder", Type: "A", Name: "example.com", Data: "192.0.2.1", TTL: 1,
			Comment: "Originless placeholder for Cloudflare redirect rules", Proxiable: true, Proxied: &proxied,
		}},
	}
	driver := NewDNSDriverWithClient(fake)
	_, err := driver.Update(context.Background(), interfaces.ResourceRef{Name: "example.com", Type: "infra.dns", ProviderID: "zone"}, interfaces.ResourceSpec{
		Name: "example.com",
		Type: "infra.dns",
		Config: map[string]any{
			"domain": "example.com",
			"records": []any{
				map[string]any{"type": "A", "name": "@", "data": "192.0.2.1", "ttl": 1},
			},
		},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(fake.updatedRecords) != 0 {
		t.Fatalf("updatedRecords = %#v, want no update when computed fields are omitted", fake.updatedRecords)
	}
}

func TestDNSDriver_DiffNormalizesRelativeRecordNames(t *testing.T) {
	driver := NewDNSDriverWithClient(&fakeCFClient{})
	current := &interfaces.ResourceOutput{
		Name:       "example.com",
		Type:       "infra.dns",
		ProviderID: "zone",
		Outputs: map[string]any{
			"domain": "example.com",
			"records": []map[string]any{
				{"type": "A", "name": "*.example.com", "data": "216.40.34.41", "ttl": 900},
				{"type": "CNAME", "name": "mail.example.com", "data": "mail.hover.com.cust.hostedemail.com", "ttl": 900},
			},
		},
	}
	diff, err := driver.Diff(context.Background(), interfaces.ResourceSpec{
		Name: "example.com",
		Type: "infra.dns",
		Config: map[string]any{
			"domain": "example.com",
			"records": []any{
				map[string]any{"type": "A", "name": "*", "data": "216.40.34.41", "ttl": 900},
				map[string]any{"type": "CNAME", "name": "mail", "data": "mail.hover.com.cust.hostedemail.com", "ttl": 900},
			},
		},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if diff.NeedsUpdate {
		t.Fatalf("diff = %#v, want no update for equivalent relative/FQDN record names", diff)
	}
}

func TestDNSDriver_DiffNormalizesTXTQuotePresentation(t *testing.T) {
	driver := NewDNSDriverWithClient(&fakeCFClient{})
	current := &interfaces.ResourceOutput{
		Name:       "example.com",
		Type:       "infra.dns",
		ProviderID: "zone",
		Outputs: map[string]any{
			"domain": "example.com",
			"records": []map[string]any{{
				"type": "TXT", "name": "example.com", "data": "google-site-verification=abc123", "ttl": 300,
			}},
		},
	}
	diff, err := driver.Diff(context.Background(), interfaces.ResourceSpec{
		Name: "example.com",
		Type: "infra.dns",
		Config: map[string]any{
			"domain": "example.com",
			"records": []any{
				map[string]any{"type": "TXT", "name": "@", "data": `"google-site-verification=abc123"`, "ttl": 300},
			},
		},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if diff.NeedsUpdate {
		t.Fatalf("diff = %#v, want no update for equivalent TXT quote presentation", diff)
	}
}

func TestDNSDriver_DiffNormalizesApexCNAMETarget(t *testing.T) {
	driver := NewDNSDriverWithClient(&fakeCFClient{})
	current := &interfaces.ResourceOutput{
		Name:       "example.com",
		Type:       "infra.dns",
		ProviderID: "zone",
		Outputs: map[string]any{
			"domain": "example.com",
			"records": []map[string]any{{
				"type": "CNAME", "name": "www.example.com", "data": "example.com", "ttl": 300,
			}},
		},
	}
	diff, err := driver.Diff(context.Background(), interfaces.ResourceSpec{
		Name: "example.com",
		Type: "infra.dns",
		Config: map[string]any{
			"domain": "example.com",
			"records": []any{
				map[string]any{"type": "CNAME", "name": "www", "data": "@", "ttl": 300},
			},
		},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if diff.NeedsUpdate {
		t.Fatalf("diff = %#v, want no update for CNAME @ target matching zone apex", diff)
	}
}

func TestDNSDriver_DiffDetectsStaleWorkflowManagedMarkersWhenUnlistedRecordsArePreserved(t *testing.T) {
	driver := NewDNSDriverWithClient(&fakeCFClient{})
	current := &interfaces.ResourceOutput{
		Name:       "gigbagg.rocks",
		Type:       "infra.dns",
		ProviderID: "zone",
		Outputs: map[string]any{
			"domain": "gigbagg.rocks",
			"records": []map[string]any{
				{
					"type":  "TXT",
					"name":  "_workflow-dns-managed.gigbagg.rocks",
					"data":  `"heritage=wfinfra-v1 managed_by=wfctl state_dir=.state/cloudflare-staging/ resource=cf-gigbagg-rocks"`,
					"ttl":   300,
					"value": `"heritage=wfinfra-v1 managed_by=wfctl state_dir=.state/cloudflare-staging/ resource=cf-gigbagg-rocks"`,
				},
				{
					"type":  "TXT",
					"name":  "_workflow-dns-managed.gigbagg.rocks",
					"data":  `"heritage=wfinfra-v1 managed_by=wfctl state_dir=.state/domain-reconcile/ resource=cf-gigbagg-rocks"`,
					"ttl":   300,
					"value": `"heritage=wfinfra-v1 managed_by=wfctl state_dir=.state/domain-reconcile/ resource=cf-gigbagg-rocks"`,
				},
				{"type": "TXT", "name": "gigbagg.rocks", "data": "external", "ttl": 300, "value": "external"},
			},
		},
	}
	diff, err := driver.Diff(context.Background(), interfaces.ResourceSpec{
		Name: "gigbagg.rocks",
		Type: "infra.dns",
		Config: map[string]any{
			"domain":          "gigbagg.rocks",
			"manage_unlisted": false,
			"records": []any{
				map[string]any{
					"type": "TXT",
					"name": "_workflow-dns-managed",
					"data": `"heritage=wfinfra-v1 managed_by=wfctl state_dir=.state/cloudflare-staging/ resource=cf-gigbagg-rocks"`,
					"ttl":  300,
				},
			},
		},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !diff.NeedsUpdate {
		t.Fatalf("diff.NeedsUpdate = false, want true for stale workflow marker")
	}
	if len(diff.Changes) != 1 {
		t.Fatalf("changes len = %d, want 1: %#v", len(diff.Changes), diff.Changes)
	}
	old, ok := diff.Changes[0].Old.(map[string]any)
	if !ok {
		t.Fatalf("change old = %#v, want record output", diff.Changes[0].Old)
	}
	if got, want := old["data"], `"heritage=wfinfra-v1 managed_by=wfctl state_dir=.state/domain-reconcile/ resource=cf-gigbagg-rocks"`; got != want {
		t.Fatalf("deleted marker data = %q, want %q", got, want)
	}
	if diff.Changes[0].New != nil {
		t.Fatalf("change new = %#v, want nil delete", diff.Changes[0].New)
	}
}

func TestDNSDriver_DiffReadsCurrentRecordValueFallback(t *testing.T) {
	driver := NewDNSDriverWithClient(&fakeCFClient{})
	current := &interfaces.ResourceOutput{
		Name:       "gigbagg.rocks",
		Type:       "infra.dns",
		ProviderID: "zone",
		Outputs: map[string]any{
			"domain": "gigbagg.rocks",
			"records": []map[string]any{
				{
					"id":    "current-marker",
					"type":  "TXT",
					"name":  "_workflow-dns-managed.gigbagg.rocks",
					"value": `"heritage=wfinfra-v1 managed_by=wfctl state_dir=.state/cloudflare-staging/ resource=cf-gigbagg-rocks"`,
					"ttl":   300,
				},
				{
					"id":    "stale-marker",
					"type":  "TXT",
					"name":  "_workflow-dns-managed.gigbagg.rocks",
					"value": `"heritage=wfinfra-v1 managed_by=wfctl state_dir=.state/domain-reconcile/ resource=cf-gigbagg-rocks"`,
					"ttl":   300,
				},
			},
		},
	}
	diff, err := driver.Diff(context.Background(), interfaces.ResourceSpec{
		Name: "gigbagg.rocks",
		Type: "infra.dns",
		Config: map[string]any{
			"domain":          "gigbagg.rocks",
			"manage_unlisted": false,
			"records": []any{
				map[string]any{
					"type": "TXT",
					"name": "_workflow-dns-managed",
					"data": `"heritage=wfinfra-v1 managed_by=wfctl state_dir=.state/cloudflare-staging/ resource=cf-gigbagg-rocks"`,
					"ttl":  300,
				},
			},
		},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !diff.NeedsUpdate {
		t.Fatalf("diff.NeedsUpdate = false, want true for stale workflow marker from value field")
	}
	if len(diff.Changes) != 1 {
		t.Fatalf("changes len = %d, want 1: %#v", len(diff.Changes), diff.Changes)
	}
	old, ok := diff.Changes[0].Old.(map[string]any)
	if !ok {
		t.Fatalf("change old = %#v, want record output", diff.Changes[0].Old)
	}
	if got, want := old["id"], "stale-marker"; got != want {
		t.Fatalf("deleted marker id = %q, want %q", got, want)
	}
}

func TestDNSDriver_DiffDetectsProductionStagingStateDrift(t *testing.T) {
	driver := NewDNSDriverWithClient(&fakeCFClient{})
	current := &interfaces.ResourceOutput{
		Name:       "cf-gigbagg-rocks",
		Type:       "infra.dns",
		ProviderID: "55779bda1d3e4e1e19459902308f4b77",
		Outputs: map[string]any{
			"domain": "gigbagg.rocks",
			"records": []any{
				map[string]any{
					"comment":   "",
					"data":      `"heritage=wfinfra-v1 managed_by=wfctl state_dir=.state/cloudflare-staging/ resource=cf-gigbagg-rocks"`,
					"id":        "e477870c0d483aa8affc372396151447",
					"name":      "_workflow-dns-managed.gigbagg.rocks",
					"priority":  0,
					"proxiable": false,
					"proxied":   false,
					"ttl":       300,
					"type":      "TXT",
				},
				map[string]any{
					"comment":   "",
					"data":      `"heritage=wfinfra-v1 managed_by=wfctl state_dir=.state/domain-reconcile/ resource=cf-gigbagg-rocks"`,
					"id":        "15c764ad42ec6c38a8a737bcab848e5f",
					"name":      "_workflow-dns-managed.gigbagg.rocks",
					"priority":  0,
					"proxiable": false,
					"proxied":   false,
					"ttl":       300,
					"type":      "TXT",
				},
			},
		},
	}
	diff, err := driver.Diff(context.Background(), interfaces.ResourceSpec{
		Name: "cf-gigbagg-rocks",
		Type: "infra.dns",
		Config: map[string]any{
			"domain":          "gigbagg.rocks",
			"manage_unlisted": false,
			"records": []any{
				map[string]any{"type": "A", "name": "*.gigbagg.rocks", "data": "216.40.34.41", "ttl": 900},
				map[string]any{"type": "A", "name": "gigbagg.rocks", "data": "216.40.34.41", "ttl": 900},
				map[string]any{"type": "CNAME", "name": "mail.gigbagg.rocks", "data": "mx.hover.com.cust.hostedemail.com", "ttl": 900},
				map[string]any{"type": "MX", "name": "gigbagg.rocks", "data": "mx.hover.com.cust.hostedemail.com", "priority": 10, "ttl": 900},
				map[string]any{
					"type": "TXT",
					"name": "_workflow-dns-managed",
					"data": `"heritage=wfinfra-v1 managed_by=wfctl state_dir=.state/cloudflare-staging/ resource=cf-gigbagg-rocks"`,
					"ttl":  300,
				},
			},
		},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !diff.NeedsUpdate {
		t.Fatal("diff.NeedsUpdate = false, want true for missing desired records and stale marker")
	}
	if len(diff.Changes) != 5 {
		t.Fatalf("changes len = %d, want 5: %#v", len(diff.Changes), diff.Changes)
	}
}

func TestDNSDriver_DiffDetectsDuplicateWorkflowManagedMarkersWithSameKey(t *testing.T) {
	driver := NewDNSDriverWithClient(&fakeCFClient{})
	current := &interfaces.ResourceOutput{
		Name:       "gigbagg.rocks",
		Type:       "infra.dns",
		ProviderID: "zone",
		Outputs: map[string]any{
			"domain": "gigbagg.rocks",
			"records": []map[string]any{
				{
					"id":   "current-marker",
					"type": "TXT",
					"name": "_workflow-dns-managed.gigbagg.rocks",
					"data": `"heritage=wfinfra-v1 managed_by=wfctl state_dir=.state/cloudflare-staging/ resource=cf-gigbagg-rocks"`,
					"ttl":  300,
				},
				{
					"id":   "duplicate-marker",
					"type": "TXT",
					"name": "_workflow-dns-managed.gigbagg.rocks",
					"data": `"heritage=wfinfra-v1 managed_by=wfctl state_dir=.state/cloudflare-staging/ resource=cf-gigbagg-rocks"`,
					"ttl":  300,
				},
			},
		},
	}
	diff, err := driver.Diff(context.Background(), interfaces.ResourceSpec{
		Name: "gigbagg.rocks",
		Type: "infra.dns",
		Config: map[string]any{
			"domain":          "gigbagg.rocks",
			"manage_unlisted": false,
			"records": []any{
				map[string]any{
					"type": "TXT",
					"name": "_workflow-dns-managed",
					"data": `"heritage=wfinfra-v1 managed_by=wfctl state_dir=.state/cloudflare-staging/ resource=cf-gigbagg-rocks"`,
					"ttl":  300,
				},
			},
		},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !diff.NeedsUpdate {
		t.Fatalf("diff.NeedsUpdate = false, want true for duplicate workflow marker")
	}
	if len(diff.Changes) != 1 {
		t.Fatalf("changes len = %d, want 1: %#v", len(diff.Changes), diff.Changes)
	}
	old, ok := diff.Changes[0].Old.(map[string]any)
	if !ok {
		t.Fatalf("change old = %#v, want record output", diff.Changes[0].Old)
	}
	if got, want := old["id"], "duplicate-marker"; got != want {
		t.Fatalf("deleted marker id = %q, want %q", got, want)
	}
}

func TestDNSDriver_DiffDetectsDuplicateWorkflowManagedMarkersWhenManagingUnlistedRecords(t *testing.T) {
	driver := NewDNSDriverWithClient(&fakeCFClient{})
	current := &interfaces.ResourceOutput{
		Name:       "gigbagg.rocks",
		Type:       "infra.dns",
		ProviderID: "zone",
		Outputs: map[string]any{
			"domain": "gigbagg.rocks",
			"records": []map[string]any{
				{
					"id":   "current-marker",
					"type": "TXT",
					"name": "_workflow-dns-managed.gigbagg.rocks",
					"data": `"heritage=wfinfra-v1 managed_by=wfctl state_dir=.state/cloudflare-staging/ resource=cf-gigbagg-rocks"`,
					"ttl":  300,
				},
				{
					"id":   "duplicate-marker",
					"type": "TXT",
					"name": "_workflow-dns-managed.gigbagg.rocks",
					"data": `"heritage=wfinfra-v1 managed_by=wfctl state_dir=.state/cloudflare-staging/ resource=cf-gigbagg-rocks"`,
					"ttl":  300,
				},
			},
		},
	}
	diff, err := driver.Diff(context.Background(), interfaces.ResourceSpec{
		Name: "gigbagg.rocks",
		Type: "infra.dns",
		Config: map[string]any{
			"domain":          "gigbagg.rocks",
			"manage_unlisted": true,
			"records": []any{
				map[string]any{
					"type": "TXT",
					"name": "_workflow-dns-managed",
					"data": `"heritage=wfinfra-v1 managed_by=wfctl state_dir=.state/cloudflare-staging/ resource=cf-gigbagg-rocks"`,
					"ttl":  300,
				},
			},
		},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !diff.NeedsUpdate {
		t.Fatalf("diff.NeedsUpdate = false, want true for duplicate workflow marker")
	}
	if len(diff.Changes) != 1 {
		t.Fatalf("changes len = %d, want 1: %#v", len(diff.Changes), diff.Changes)
	}
	old, ok := diff.Changes[0].Old.(map[string]any)
	if !ok {
		t.Fatalf("change old = %#v, want record output", diff.Changes[0].Old)
	}
	if got, want := old["id"], "duplicate-marker"; got != want {
		t.Fatalf("deleted marker id = %q, want %q", got, want)
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

func TestDNSDriver_UpdateChangesMXPriorityInPlace(t *testing.T) {
	fake := &fakeCFClient{
		zone: &Zone{ID: "zone", Name: "example.com"},
		records: []Record{
			{ID: "mx-primary", Type: "MX", Name: "example.com", Data: "mail.protonmail.ch", TTL: 300, Priority: 0},
		},
	}
	driver := NewDNSDriverWithClient(fake)
	_, err := driver.Update(context.Background(), interfaces.ResourceRef{Name: "example.com", Type: "infra.dns", ProviderID: "zone"}, interfaces.ResourceSpec{
		Name: "example.com",
		Type: "infra.dns",
		Config: map[string]any{
			"domain": "example.com",
			"records": []any{
				map[string]any{"type": "MX", "name": "@", "data": "mail.protonmail.ch", "ttl": 300, "priority": 10},
			},
		},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(fake.createdRecords) != 0 {
		t.Fatalf("createdRecords = %#v, want none", fake.createdRecords)
	}
	if len(fake.updatedRecords) != 1 {
		t.Fatalf("updatedRecords = %#v, want one MX priority update", fake.updatedRecords)
	}
	if got := fake.updatedRecords[0]; got.ID != "mx-primary" || got.Priority != 10 {
		t.Fatalf("updated MX = %#v, want existing mx-primary priority 10", got)
	}
}

func TestDNSDriver_UpdateNormalizesRelativeRecordNamesBeforeMatching(t *testing.T) {
	fake := &fakeCFClient{
		zone: &Zone{ID: "zone", Name: "example.com"},
		records: []Record{
			{ID: "wildcard", Type: "A", Name: "*.example.com", Data: "216.40.34.41", TTL: 900},
			{ID: "mail", Type: "CNAME", Name: "mail.example.com", Data: "mail.hover.com.cust.hostedemail.com", TTL: 900},
		},
	}
	driver := NewDNSDriverWithClient(fake)
	_, err := driver.Update(context.Background(), interfaces.ResourceRef{Name: "example.com", Type: "infra.dns", ProviderID: "zone"}, interfaces.ResourceSpec{
		Name: "example.com",
		Type: "infra.dns",
		Config: map[string]any{
			"domain": "example.com",
			"records": []any{
				map[string]any{"type": "A", "name": "*", "data": "216.40.34.41", "ttl": 900},
				map[string]any{"type": "CNAME", "name": "mail", "data": "mail.hover.com.cust.hostedemail.com", "ttl": 900},
			},
		},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(fake.createdRecords) != 0 {
		t.Fatalf("createdRecords = %#v, want none", fake.createdRecords)
	}
}

func TestDNSDriver_UpdateNormalizesApexCNAMETargetBeforeMatching(t *testing.T) {
	fake := &fakeCFClient{
		zone: &Zone{ID: "zone", Name: "example.com"},
		records: []Record{
			{ID: "www", Type: "CNAME", Name: "www.example.com", Data: "example.com", TTL: 300},
		},
	}
	driver := NewDNSDriverWithClient(fake)
	_, err := driver.Update(context.Background(), interfaces.ResourceRef{Name: "example.com", Type: "infra.dns", ProviderID: "zone"}, interfaces.ResourceSpec{
		Name: "example.com",
		Type: "infra.dns",
		Config: map[string]any{
			"domain": "example.com",
			"records": []any{
				map[string]any{"type": "CNAME", "name": "www", "data": "@", "ttl": 300},
			},
		},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(fake.createdRecords) != 0 {
		t.Fatalf("createdRecords = %#v, want none", fake.createdRecords)
	}
	if len(fake.updatedRecords) != 0 {
		t.Fatalf("updatedRecords = %#v, want none", fake.updatedRecords)
	}
}

func TestDNSDriver_UpdateNormalizesRelativeCurrentTXTMarkerBeforeMatching(t *testing.T) {
	fake := &fakeCFClient{
		zone: &Zone{ID: "zone", Name: "example.com"},
		records: []Record{
			{
				ID:   "managed",
				Type: "TXT",
				Name: "_workflow-dns-managed",
				Data: "heritage=wfinfra-v1 managed_by=wfctl state_dir=.state/cloudflare-staging/ resource=cf-example-com",
				TTL:  300,
			},
		},
	}
	driver := NewDNSDriverWithClient(fake)
	_, err := driver.Update(context.Background(), interfaces.ResourceRef{Name: "example.com", Type: "infra.dns", ProviderID: "zone"}, interfaces.ResourceSpec{
		Name: "example.com",
		Type: "infra.dns",
		Config: map[string]any{
			"domain": "example.com",
			"records": []any{
				map[string]any{
					"type": "TXT",
					"name": "_workflow-dns-managed.example.com",
					"data": `"heritage=wfinfra-v1 managed_by=wfctl state_dir=.state/cloudflare-staging/ resource=cf-example-com"`,
					"ttl":  300,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(fake.createdRecords) != 0 {
		t.Fatalf("createdRecords = %#v, want none", fake.createdRecords)
	}
	if len(fake.updatedRecords) != 0 {
		t.Fatalf("updatedRecords = %#v, want none", fake.updatedRecords)
	}
}

func TestDNSDriver_UpdateDedupesEquivalentDesiredTXTMarkers(t *testing.T) {
	fake := &fakeCFClient{
		zone: &Zone{ID: "zone", Name: "example.com"},
		records: []Record{
			{
				ID:   "managed",
				Type: "TXT",
				Name: "_workflow-dns-managed.example.com",
				Data: "heritage=wfinfra-v1 managed_by=wfctl state_dir=.state/cloudflare-staging/ resource=cf-example-com",
				TTL:  300,
			},
		},
	}
	driver := NewDNSDriverWithClient(fake)
	_, err := driver.Update(context.Background(), interfaces.ResourceRef{Name: "example.com", Type: "infra.dns", ProviderID: "zone"}, interfaces.ResourceSpec{
		Name: "example.com",
		Type: "infra.dns",
		Config: map[string]any{
			"domain": "example.com",
			"records": []any{
				map[string]any{
					"type": "TXT",
					"name": "_workflow-dns-managed.example.com",
					"data": `"heritage=wfinfra-v1 managed_by=wfctl state_dir=.state/cloudflare-staging/ resource=cf-example-com"`,
					"ttl":  300,
				},
				map[string]any{
					"type": "TXT",
					"name": "_workflow-dns-managed",
					"data": `"heritage=wfinfra-v1 managed_by=wfctl state_dir=.state/cloudflare-staging/ resource=cf-example-com"`,
					"ttl":  300,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(fake.createdRecords) != 0 {
		t.Fatalf("createdRecords = %#v, want none", fake.createdRecords)
	}
	if len(fake.updatedRecords) != 0 {
		t.Fatalf("updatedRecords = %#v, want none", fake.updatedRecords)
	}
}

func TestDNSDriver_DiffRejectsConflictingDuplicateDesiredRecords(t *testing.T) {
	driver := NewDNSDriverWithClient(&fakeCFClient{})
	_, err := driver.Diff(context.Background(), interfaces.ResourceSpec{
		Name: "example.com",
		Type: "infra.dns",
		Config: map[string]any{
			"domain": "example.com",
			"records": []any{
				map[string]any{"type": "TXT", "name": "_workflow-dns-managed.example.com", "data": `"same"`, "ttl": 300},
				map[string]any{"type": "TXT", "name": "_workflow-dns-managed", "data": `"same"`, "ttl": 600},
			},
		},
	}, &interfaces.ResourceOutput{
		Name:       "example.com",
		Type:       "infra.dns",
		ProviderID: "zone",
		Outputs: map[string]any{
			"domain":  "example.com",
			"records": []map[string]any{},
		},
	})
	if err == nil {
		t.Fatal("Diff returned nil error, want conflicting duplicate error")
	}
	if !strings.Contains(err.Error(), "records contain conflicting duplicate TXT record") {
		t.Fatalf("Diff error = %v, want records contain conflicting duplicate TXT record", err)
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

func TestDNSDriver_UpdateDeletesConflictingAddressRecordBeforeCreatingCNAME(t *testing.T) {
	proxied := true
	fake := &fakeCFClient{
		zone: &Zone{ID: "zone", Name: "blackorchid-tributeband.com"},
		records: []Record{
			{ID: "wildcard-a", Type: "A", Name: "*.blackorchid-tributeband.com", Data: "216.40.34.41", TTL: 900},
		},
	}
	driver := NewDNSDriverWithClient(fake)
	_, err := driver.Update(context.Background(), interfaces.ResourceRef{Name: "blackorchid-tributeband.com", Type: "infra.dns", ProviderID: "zone"}, interfaces.ResourceSpec{
		Name: "blackorchid-tributeband.com",
		Type: "infra.dns",
		Config: map[string]any{
			"domain":          "blackorchid-tributeband.com",
			"manage_unlisted": true,
			"records": []any{
				map[string]any{
					"type":    "CNAME",
					"name":    "*",
					"data":    "gocodealone-multisite-zeqkn.ondigitalocean.app",
					"ttl":     1,
					"proxied": true,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(fake.createdRecords) != 1 {
		t.Fatalf("createdRecords = %#v, want one CNAME", fake.createdRecords)
	}
	if fake.createdRecords[0].Type != "CNAME" || fake.createdRecords[0].Proxied == nil || *fake.createdRecords[0].Proxied != proxied {
		t.Fatalf("createdRecords[0] = %#v, want proxied CNAME", fake.createdRecords[0])
	}
	want := []string{
		"delete:wildcard-a",
		"create:CNAME:*.blackorchid-tributeband.com",
	}
	if !slices.Equal(fake.operations, want) {
		t.Fatalf("operations = %#v, want %#v", fake.operations, want)
	}
}

func TestDNSDriver_UpdateDeletesStaleWorkflowManagedMarkersWhenUnlistedRecordsArePreserved(t *testing.T) {
	fake := &fakeCFClient{
		zone: &Zone{ID: "zone", Name: "gigbagg.rocks"},
		records: []Record{
			{
				ID:   "current-marker",
				Type: "TXT",
				Name: "_workflow-dns-managed.gigbagg.rocks",
				Data: "heritage=wfinfra-v1 managed_by=wfctl state_dir=.state/cloudflare-staging/ resource=cf-gigbagg-rocks",
				TTL:  300,
			},
			{
				ID:   "stale-marker",
				Type: "TXT",
				Name: "_workflow-dns-managed.gigbagg.rocks",
				Data: "heritage=wfinfra-v1 managed_by=wfctl state_dir=.state/domain-reconcile/ resource=cf-gigbagg-rocks",
				TTL:  300,
			},
			{ID: "unmanaged", Type: "TXT", Name: "gigbagg.rocks", Data: "external", TTL: 300},
		},
	}
	driver := NewDNSDriverWithClient(fake)
	_, err := driver.Update(context.Background(), interfaces.ResourceRef{Name: "gigbagg.rocks", Type: "infra.dns", ProviderID: "zone"}, interfaces.ResourceSpec{
		Name: "gigbagg.rocks",
		Type: "infra.dns",
		Config: map[string]any{
			"domain":          "gigbagg.rocks",
			"manage_unlisted": false,
			"records": []any{
				map[string]any{
					"type": "TXT",
					"name": "_workflow-dns-managed",
					"data": `"heritage=wfinfra-v1 managed_by=wfctl state_dir=.state/cloudflare-staging/ resource=cf-gigbagg-rocks"`,
					"ttl":  300,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(fake.deletedRecords) != 1 || fake.deletedRecords[0] != "stale-marker" {
		t.Fatalf("deletedRecords = %#v, want stale-marker only", fake.deletedRecords)
	}
}

func TestDNSDriver_UpdateDeletesDuplicateWorkflowManagedMarkersWithSameKey(t *testing.T) {
	fake := &fakeCFClient{
		zone: &Zone{ID: "zone", Name: "gigbagg.rocks"},
		records: []Record{
			{
				ID:   "current-marker",
				Type: "TXT",
				Name: "_workflow-dns-managed.gigbagg.rocks",
				Data: "heritage=wfinfra-v1 managed_by=wfctl state_dir=.state/cloudflare-staging/ resource=cf-gigbagg-rocks",
				TTL:  300,
			},
			{
				ID:   "duplicate-marker",
				Type: "TXT",
				Name: "_workflow-dns-managed.gigbagg.rocks",
				Data: "heritage=wfinfra-v1 managed_by=wfctl state_dir=.state/cloudflare-staging/ resource=cf-gigbagg-rocks",
				TTL:  300,
			},
			{ID: "unmanaged", Type: "TXT", Name: "gigbagg.rocks", Data: "external", TTL: 300},
		},
	}
	driver := NewDNSDriverWithClient(fake)
	_, err := driver.Update(context.Background(), interfaces.ResourceRef{Name: "gigbagg.rocks", Type: "infra.dns", ProviderID: "zone"}, interfaces.ResourceSpec{
		Name: "gigbagg.rocks",
		Type: "infra.dns",
		Config: map[string]any{
			"domain":          "gigbagg.rocks",
			"manage_unlisted": false,
			"records": []any{
				map[string]any{
					"type": "TXT",
					"name": "_workflow-dns-managed",
					"data": `"heritage=wfinfra-v1 managed_by=wfctl state_dir=.state/cloudflare-staging/ resource=cf-gigbagg-rocks"`,
					"ttl":  300,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(fake.deletedRecords) != 1 || fake.deletedRecords[0] != "duplicate-marker" {
		t.Fatalf("deletedRecords = %#v, want duplicate-marker only", fake.deletedRecords)
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
