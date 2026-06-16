package drivers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"time"

	"github.com/GoCodeAlone/workflow/interfaces"
	cloudflare "github.com/cloudflare/cloudflare-go/v7"
	cfdns "github.com/cloudflare/cloudflare-go/v7/dns"
	"github.com/cloudflare/cloudflare-go/v7/option"
	"github.com/cloudflare/cloudflare-go/v7/zones"
)

// CloudflareClient is the testable subset of Cloudflare DNS/zone operations.
type CloudflareClient interface {
	GetZone(ctx context.Context, domain, zoneID string) (*Zone, error)
	CreateZone(ctx context.Context, accountID, domain string) (*Zone, error)
	DeleteZone(ctx context.Context, zoneID string) error
	ListRecords(ctx context.Context, zoneID string) ([]Record, error)
	CreateRecord(ctx context.Context, zoneID string, record Record) (*Record, error)
	UpdateRecord(ctx context.Context, zoneID, recordID string, record Record) (*Record, error)
	DeleteRecord(ctx context.Context, zoneID, recordID string) error
	GetDNSSEC(ctx context.Context, zoneID string) (*DNSSEC, error)
}

// Zone is the Cloudflare zone metadata preserved in state outputs.
type Zone struct {
	ID                  string
	Name                string
	Status              string
	NameServers         []string
	OriginalNameServers []string
	OriginalRegistrar   string
	OriginalDNSHost     string
}

// DNSSEC is a compact DNSSEC snapshot.
type DNSSEC struct {
	Status          string
	DS              string
	KeyTag          int
	Algorithm       string
	Digest          string
	DigestAlgorithm string
}

// Record is the provider-independent internal shape used by this driver.
type Record struct {
	ID        string
	Type      string
	Name      string
	Data      string
	TTL       int
	Priority  int
	Proxied   *bool
	Proxiable bool
	Comment   string
	Tags      []string
}

type DNSDriver struct {
	client           CloudflareClient
	defaultAccountID string
	operationTimeout time.Duration
}

const (
	defaultRequestTimeout   = 30 * time.Second
	defaultOperationTimeout = 2 * time.Minute
)

// NormalizeRequestTimeout returns the per-HTTP-request Cloudflare API timeout,
// falling back to the provider default when callers pass a zero value.
func NormalizeRequestTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return defaultRequestTimeout
	}
	return timeout
}

// NormalizeOperationTimeout returns the Cloudflare API operation timeout,
// falling back to the provider default when callers pass a zero value.
func NormalizeOperationTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return defaultOperationTimeout
	}
	return timeout
}

func (d *DNSDriver) operationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, NormalizeOperationTimeout(d.operationTimeout))
}

func NewDNSDriver(apiToken string) *DNSDriver {
	return NewDNSDriverWithAccountRequestTimeout(apiToken, "", 0)
}

func NewDNSDriverWithAccount(apiToken, accountID string) *DNSDriver {
	return NewDNSDriverWithAccountRequestTimeout(apiToken, accountID, 0)
}

func NewDNSDriverWithAccountTimeout(apiToken, accountID string, operationTimeout time.Duration) *DNSDriver {
	return newDNSDriverWithAccountTimeouts(apiToken, accountID, 0, operationTimeout)
}

func NewDNSDriverWithAccountRequestTimeout(apiToken, accountID string, requestTimeout time.Duration) *DNSDriver {
	return newDNSDriverWithAccountTimeouts(apiToken, accountID, requestTimeout, 0)
}

func newDNSDriverWithAccountTimeouts(apiToken, accountID string, requestTimeout, operationTimeout time.Duration) *DNSDriver {
	return &DNSDriver{
		client:           newSDKClient(apiToken, NormalizeRequestTimeout(requestTimeout)),
		defaultAccountID: strings.TrimSpace(accountID),
		operationTimeout: NormalizeOperationTimeout(operationTimeout),
	}
}

func NewDNSDriverWithClient(client CloudflareClient) *DNSDriver {
	return &DNSDriver{client: client, operationTimeout: defaultOperationTimeout}
}

func NewDNSDriverWithClientAndAccount(client CloudflareClient, accountID string) *DNSDriver {
	return NewDNSDriverWithClientAndAccountTimeout(client, accountID, defaultOperationTimeout)
}

func NewDNSDriverWithClientAndAccountTimeout(client CloudflareClient, accountID string, operationTimeout time.Duration) *DNSDriver {
	timeout := NormalizeOperationTimeout(operationTimeout)
	return &DNSDriver{client: client, defaultAccountID: strings.TrimSpace(accountID), operationTimeout: timeout}
}

func (d *DNSDriver) Create(ctx context.Context, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	ctx, cancel := d.operationContext(ctx)
	defer cancel()
	parsed, err := parseDNSSpec(spec, true, d.defaultAccountID)
	if err != nil {
		return nil, fmt.Errorf("cloudflare dns create %q: %w", spec.Name, err)
	}
	zone, err := d.ensureZone(ctx, parsed)
	if err != nil {
		return nil, fmt.Errorf("cloudflare dns create %q: %w", spec.Name, err)
	}
	if err := d.applyRecords(ctx, zone.ID, zone.Name, parsed.Records, parsed.ManageUnlisted); err != nil {
		return nil, fmt.Errorf("cloudflare dns create %q: %w", spec.Name, err)
	}
	return d.readOutput(ctx, spec.Name, zone)
}

func (d *DNSDriver) Read(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.ResourceOutput, error) {
	ctx, cancel := d.operationContext(ctx)
	defer cancel()
	domain, zoneID := domainAndZoneIDFromRef(ref)
	zone, err := d.client.GetZone(ctx, domain, zoneID)
	if err != nil {
		if isCloudflareNotFound(err) {
			return nil, fmt.Errorf("%w: cloudflare dns read %q: %w", interfaces.ErrResourceNotFound, ref.Name, err)
		}
		return nil, fmt.Errorf("cloudflare dns read %q: %w", ref.Name, err)
	}
	return d.readOutput(ctx, ref.Name, zone)
}

func (d *DNSDriver) Update(ctx context.Context, ref interfaces.ResourceRef, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	ctx, cancel := d.operationContext(ctx)
	defer cancel()
	parsed, err := parseDNSSpec(spec, true, d.defaultAccountID)
	if err != nil {
		return nil, fmt.Errorf("cloudflare dns update %q: %w", ref.Name, err)
	}
	zone, err := d.client.GetZone(ctx, parsed.Domain, ref.ProviderID)
	if err != nil {
		return nil, fmt.Errorf("cloudflare dns update %q: %w", ref.Name, err)
	}
	if !strings.EqualFold(parsed.Domain, zone.Name) {
		return nil, fmt.Errorf("cloudflare dns update %q: cannot change domain from %q to %q", ref.Name, zone.Name, parsed.Domain)
	}
	if err := d.applyRecords(ctx, zone.ID, zone.Name, parsed.Records, parsed.ManageUnlisted); err != nil {
		return nil, fmt.Errorf("cloudflare dns update %q: %w", ref.Name, err)
	}
	return d.readOutput(ctx, ref.Name, zone)
}

func (d *DNSDriver) Delete(ctx context.Context, ref interfaces.ResourceRef) error {
	ctx, cancel := d.operationContext(ctx)
	defer cancel()
	zoneID := ref.ProviderID
	if zoneID == "" {
		zone, err := d.client.GetZone(ctx, ref.Name, "")
		if err != nil {
			return fmt.Errorf("cloudflare dns delete %q: %w", ref.Name, err)
		}
		zoneID = zone.ID
	}
	if err := d.client.DeleteZone(ctx, zoneID); err != nil {
		return fmt.Errorf("cloudflare dns delete %q: %w", ref.Name, err)
	}
	return nil
}

func (d *DNSDriver) Diff(_ context.Context, desired interfaces.ResourceSpec, current *interfaces.ResourceOutput) (*interfaces.DiffResult, error) {
	if current == nil {
		return &interfaces.DiffResult{NeedsUpdate: true}, nil
	}
	parsed, err := parseDNSSpec(desired, true, d.defaultAccountID)
	if err != nil {
		return nil, fmt.Errorf("cloudflare dns diff: %w", err)
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
	currentRecords, err := recordsFromOutputs(current.Outputs)
	if err != nil {
		return nil, err
	}
	changes := diffRecords(currentRecords, parsed.Records, parsed.ManageUnlisted, parsed.Domain)
	return &interfaces.DiffResult{NeedsUpdate: len(changes) > 0, Changes: changes}, nil
}

func (d *DNSDriver) HealthCheck(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.HealthResult, error) {
	ctx, cancel := d.operationContext(ctx)
	defer cancel()
	if _, err := d.client.GetZone(ctx, ref.Name, ref.ProviderID); err != nil {
		return &interfaces.HealthResult{Healthy: false, Message: err.Error()}, nil
	}
	return &interfaces.HealthResult{Healthy: true, Message: "ok"}, nil
}

func (d *DNSDriver) Scale(ctx context.Context, ref interfaces.ResourceRef, _ int) (*interfaces.ResourceOutput, error) {
	return d.Read(ctx, ref)
}

func (d *DNSDriver) Type() string { return "infra.dns" }

func (d *DNSDriver) SensitiveKeys() []string { return nil }

func (d *DNSDriver) ProviderIDFormat() interfaces.ProviderIDFormat {
	return interfaces.IDFormatDomainName
}

func (d *DNSDriver) ensureZone(ctx context.Context, parsed dnsSpec) (*Zone, error) {
	zone, err := d.client.GetZone(ctx, parsed.Domain, parsed.ZoneID)
	if err == nil {
		return zone, nil
	}
	if !isCloudflareNotFound(err) {
		return nil, fmt.Errorf("lookup zone %q: %w", parsed.Domain, err)
	}
	if parsed.AccountID == "" {
		return nil, fmt.Errorf("lookup zone %q: %w", parsed.Domain, err)
	}
	zone, err = d.client.CreateZone(ctx, parsed.AccountID, parsed.Domain)
	if err != nil {
		return nil, fmt.Errorf("create zone %q: %w", parsed.Domain, err)
	}
	return zone, nil
}

func (d *DNSDriver) AdoptionRef(spec interfaces.ResourceSpec) (interfaces.ResourceRef, bool, error) {
	parsed, err := parseDNSSpec(spec, true, d.defaultAccountID)
	if err != nil {
		return interfaces.ResourceRef{}, false, err
	}
	return interfaces.ResourceRef{Name: parsed.Domain, Type: "infra.dns", ProviderID: parsed.Domain}, true, nil
}

func isCloudflareNotFound(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, interfaces.ErrResourceNotFound) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "not found")
}

func (d *DNSDriver) readOutput(ctx context.Context, name string, zone *Zone) (*interfaces.ResourceOutput, error) {
	records, err := d.client.ListRecords(ctx, zone.ID)
	if err != nil {
		return nil, fmt.Errorf("list records for zone %q: %w", zone.ID, err)
	}
	dnssec, _ := d.client.GetDNSSEC(ctx, zone.ID)
	return dnsOutput(name, zone, records, dnssec), nil
}

func (d *DNSDriver) applyRecords(ctx context.Context, zoneID, zoneName string, desired []Record, manageUnlisted bool) error {
	current, err := d.client.ListRecords(ctx, zoneID)
	if err != nil {
		return fmt.Errorf("list records for zone %q: %w", zoneID, err)
	}
	currentByKey := recordsByKey(current, zoneName)
	desiredKeys := map[string]struct{}{}
	for _, record := range desired {
		key := recordKey(record, zoneName)
		desiredKeys[key] = struct{}{}
		existing := currentByKey[key]
		if len(existing) == 0 {
			if _, err := d.client.CreateRecord(ctx, zoneID, record); err != nil {
				return fmt.Errorf("create %s record %q in zone %q: %w", record.Type, record.Name, zoneID, err)
			}
			continue
		}
		currentByKey[key] = existing[1:]
		if !recordMatches(existing[0], record, zoneName) {
			if _, err := d.client.UpdateRecord(ctx, zoneID, existing[0].ID, record); err != nil {
				return fmt.Errorf("update %s record %q in zone %q: %w", record.Type, record.Name, zoneID, err)
			}
		}
	}
	if !manageUnlisted {
		return nil
	}
	for _, record := range current {
		if _, ok := desiredKeys[recordKey(record, zoneName)]; ok {
			continue
		}
		if err := d.client.DeleteRecord(ctx, zoneID, record.ID); err != nil {
			return fmt.Errorf("delete %s record %q in zone %q: %w", record.Type, record.Name, zoneID, err)
		}
	}
	return nil
}

type dnsSpec struct {
	Domain         string
	ZoneID         string
	AccountID      string
	ManageUnlisted bool
	Records        []Record
}

func parseDNSSpec(spec interfaces.ResourceSpec, requireRecords bool, defaultAccountID string) (dnsSpec, error) {
	domain, _ := spec.Config["domain"].(string)
	if domain == "" {
		domain = spec.Name
	}
	if domain == "" || !interfaces.ValidateProviderID(domain, interfaces.IDFormatDomainName) {
		return dnsSpec{}, fmt.Errorf("domain %q is not a valid domain name", domain)
	}
	parsed := dnsSpec{Domain: domain, AccountID: strings.TrimSpace(defaultAccountID)}
	parsed.ZoneID, _ = spec.Config["zone_id"].(string)
	if accountID, _ := spec.Config["account_id"].(string); strings.TrimSpace(accountID) != "" {
		parsed.AccountID = strings.TrimSpace(accountID)
	}
	if b, ok := spec.Config["manage_unlisted"].(bool); ok {
		parsed.ManageUnlisted = b
	}
	rawValue, ok := spec.Config["records"]
	if !ok {
		if requireRecords {
			return dnsSpec{}, fmt.Errorf("records is required (use explicit records: [] for an empty zone)")
		}
		return parsed, nil
	}
	rawRecords, err := recordConfigMaps(rawValue)
	if err != nil {
		return dnsSpec{}, err
	}
	parsed.Records = make([]Record, 0, len(rawRecords))
	for i, raw := range rawRecords {
		record, err := recordFromConfig(i, raw, domain)
		if err != nil {
			return dnsSpec{}, err
		}
		parsed.Records = append(parsed.Records, record)
	}
	records, err := dedupeDesiredRecords(parsed.Records, domain)
	if err != nil {
		return dnsSpec{}, err
	}
	parsed.Records = records
	return parsed, nil
}

func recordConfigMaps(rawValue any) ([]map[string]any, error) {
	switch raw := rawValue.(type) {
	case []map[string]any:
		return raw, nil
	case []any:
		out := make([]map[string]any, 0, len(raw))
		for i, item := range raw {
			m, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("records[%d] must be an object", i)
			}
			out = append(out, m)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("records must be a list")
	}
}

func recordFromConfig(index int, m map[string]any, domain string) (Record, error) {
	recordType, err := stringField(index, m, "type", "")
	if err != nil {
		return Record{}, err
	}
	recordType = strings.ToUpper(recordType)
	if !supportedRecordType(recordType) {
		return Record{}, fmt.Errorf("records[%d].type %q is not supported", index, recordType)
	}
	name, err := stringField(index, m, "name", "@")
	if err != nil {
		return Record{}, err
	}
	data, err := stringField(index, m, "data", "")
	if err != nil {
		return Record{}, err
	}
	data = rawTXTData(recordType, data)
	ttl, err := intField(index, m, "ttl", 1)
	if err != nil {
		return Record{}, err
	}
	priority, err := intField(index, m, "priority", 0)
	if err != nil {
		return Record{}, err
	}
	comment, err := stringField(index, m, "comment", "")
	if err != nil {
		return Record{}, err
	}
	var proxied *bool
	if raw, ok := m["proxied"]; ok {
		value, ok := raw.(bool)
		if !ok {
			return Record{}, fmt.Errorf("records[%d].proxied must be a bool", index)
		}
		proxied = &value
	}
	record := Record{Type: recordType, Name: normalizeRecordName(name, domain), Data: data, TTL: ttl, Priority: priority, Proxied: proxied, Comment: comment}
	if err := validateRecord(index, record); err != nil {
		return Record{}, err
	}
	return record, nil
}

func stringField(index int, m map[string]any, key, defaultValue string) (string, error) {
	raw, ok := m[key]
	if !ok {
		return defaultValue, nil
	}
	value, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("records[%d].%s must be a string", index, key)
	}
	if value == "" && defaultValue == "" && (key == "type" || key == "data") {
		return "", fmt.Errorf("records[%d].%s is required", index, key)
	}
	return value, nil
}

func intField(index int, m map[string]any, key string, defaultValue int) (int, error) {
	raw, ok := m[key]
	if !ok {
		return defaultValue, nil
	}
	switch v := raw.(type) {
	case int:
		return v, nil
	case int64:
		return int(v), nil
	case float64:
		if v != float64(int(v)) {
			return 0, fmt.Errorf("records[%d].%s must be an integer", index, key)
		}
		return int(v), nil
	default:
		return 0, fmt.Errorf("records[%d].%s must be an integer", index, key)
	}
}

func validateRecord(index int, record Record) error {
	if record.Name == "" {
		return fmt.Errorf("records[%d].name is required", index)
	}
	if record.TTL != 1 && (record.TTL < 60 || record.TTL > 86400) {
		return fmt.Errorf("records[%d].ttl must be 1 or between 60 and 86400", index)
	}
	switch record.Type {
	case "A":
		addr, err := netip.ParseAddr(record.Data)
		if err != nil || !addr.Is4() {
			return fmt.Errorf("records[%d].data must be an IPv4 address", index)
		}
	case "AAAA":
		addr, err := netip.ParseAddr(record.Data)
		if err != nil || !addr.Is6() {
			return fmt.Errorf("records[%d].data must be an IPv6 address", index)
		}
	case "MX":
		if record.Priority < 0 {
			return fmt.Errorf("records[%d].priority must be non-negative", index)
		}
	}
	return nil
}

func supportedRecordType(recordType string) bool {
	switch recordType {
	case "A", "AAAA", "CAA", "CNAME", "MX", "NS", "SRV", "TXT":
		return true
	default:
		return false
	}
}

func normalizeRecordName(name, domain string) string {
	trimmed := strings.TrimSuffix(name, ".")
	if trimmed == "@" {
		return domain
	}
	if trimmed == "" || strings.EqualFold(trimmed, domain) || strings.HasSuffix(strings.ToLower(trimmed), "."+strings.ToLower(domain)) {
		return trimmed
	}
	return trimmed + "." + domain
}

func recordsByKey(records []Record, domain string) map[string][]Record {
	out := make(map[string][]Record)
	for _, record := range records {
		key := recordKey(record, domain)
		out[key] = append(out[key], record)
	}
	return out
}

func dedupeDesiredRecords(records []Record, domain string) ([]Record, error) {
	out := make([]Record, 0, len(records))
	seen := make(map[string]Record, len(records))
	for _, record := range records {
		key := recordKey(record, domain)
		existing, ok := seen[key]
		if !ok {
			seen[key] = record
			out = append(out, record)
			continue
		}
		if !recordMatches(existing, record, domain) {
			return nil, fmt.Errorf("records contains conflicting duplicate %s record %q", record.Type, record.Name)
		}
	}
	return out, nil
}

func diffRecords(current, desired []Record, manageUnlisted bool, domain string) []interfaces.FieldChange {
	var changes []interfaces.FieldChange
	currentByKey := recordsByKey(current, domain)
	desiredKeys := map[string]struct{}{}
	for _, record := range desired {
		key := recordKey(record, domain)
		desiredKeys[key] = struct{}{}
		candidates := currentByKey[key]
		if len(candidates) == 0 {
			changes = append(changes, interfaces.FieldChange{Path: "records", Old: nil, New: recordOutput(record)})
			continue
		}
		current := candidates[0]
		currentByKey[key] = candidates[1:]
		if !recordMatches(current, record, domain) {
			changes = append(changes, interfaces.FieldChange{Path: "records", Old: recordOutput(current), New: recordOutput(record)})
		}
	}
	if manageUnlisted {
		for _, record := range current {
			if _, ok := desiredKeys[recordKey(record, domain)]; !ok {
				changes = append(changes, interfaces.FieldChange{Path: "records", Old: recordOutput(record), New: nil})
			}
		}
	}
	return changes
}

func recordMatches(current, desired Record, domain string) bool {
	if !strings.EqualFold(current.Type, desired.Type) ||
		canonicalName(current.Name, domain) != canonicalName(desired.Name, domain) ||
		canonicalData(current.Type, current.Data) != canonicalData(desired.Type, desired.Data) ||
		current.TTL != desired.TTL ||
		current.Priority != desired.Priority ||
		current.Comment != desired.Comment {
		return false
	}
	if desired.Proxied != nil && boolPtrValue(current.Proxied) != *desired.Proxied {
		return false
	}
	return true
}

func domainAndZoneIDFromRef(ref interfaces.ResourceRef) (string, string) {
	if ref.ProviderID != "" && interfaces.ValidateProviderID(ref.ProviderID, interfaces.IDFormatDomainName) {
		return ref.ProviderID, ""
	}
	return ref.Name, ref.ProviderID
}

func recordKey(record Record, domain string) string {
	parts := []string{strings.ToUpper(record.Type), canonicalName(record.Name, domain), canonicalData(record.Type, record.Data)}
	if strings.EqualFold(record.Type, "MX") || strings.EqualFold(record.Type, "SRV") {
		parts = append(parts, fmt.Sprint(record.Priority))
	}
	return strings.Join(parts, "\x00")
}

func canonicalName(name, domain string) string {
	return strings.ToLower(normalizeRecordName(strings.TrimSpace(name), strings.TrimSpace(domain)))
}

func canonicalData(recordType, data string) string {
	switch strings.ToUpper(recordType) {
	case "CNAME", "MX", "NS", "SRV":
		return strings.ToLower(strings.TrimSuffix(data, "."))
	case "TXT":
		return rawTXTData(recordType, data)
	default:
		return data
	}
}

func rawTXTData(recordType, data string) string {
	if !strings.EqualFold(recordType, "TXT") {
		return data
	}
	if len(data) >= 2 && data[0] == '"' && data[len(data)-1] == '"' {
		return data[1 : len(data)-1]
	}
	return data
}

func presentationTXTData(recordType, data string) string {
	if !strings.EqualFold(recordType, "TXT") {
		return data
	}
	return `"` + rawTXTData(recordType, data) + `"`
}

func boolPtrValue(v *bool) bool {
	return v != nil && *v
}

func dnsOutput(name string, zone *Zone, records []Record, dnssec *DNSSEC) *interfaces.ResourceOutput {
	outputs := map[string]any{
		"zone_id":               zone.ID,
		"domain":                zone.Name,
		"status":                zone.Status,
		"name_servers":          append([]string(nil), zone.NameServers...),
		"original_name_servers": append([]string(nil), zone.OriginalNameServers...),
		"original_registrar":    zone.OriginalRegistrar,
		"original_dnshost":      zone.OriginalDNSHost,
		"record_count":          len(records),
		"records":               recordOutputs(records),
		"authority": map[string]any{
			"role":                  "target_authoritative_dns",
			"dns_host":              "Cloudflare",
			"name_servers":          append([]string(nil), zone.NameServers...),
			"original_name_servers": append([]string(nil), zone.OriginalNameServers...),
			"original_registrar":    zone.OriginalRegistrar,
			"original_dnshost":      zone.OriginalDNSHost,
		},
	}
	if dnssec != nil {
		outputs["dnssec"] = map[string]any{
			"status":           dnssec.Status,
			"ds":               dnssec.DS,
			"key_tag":          dnssec.KeyTag,
			"algorithm":        dnssec.Algorithm,
			"digest":           dnssec.Digest,
			"digest_algorithm": dnssec.DigestAlgorithm,
		}
	}
	return &interfaces.ResourceOutput{
		Name:       name,
		Type:       "infra.dns",
		ProviderID: zone.ID,
		Outputs:    outputs,
		Status:     "active",
	}
}

func recordOutputs(records []Record) []map[string]any {
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		out = append(out, recordOutput(record))
	}
	sort.SliceStable(out, func(i, j int) bool {
		return fmt.Sprint(out[i]["type"], out[i]["name"], out[i]["data"], out[i]["priority"]) <
			fmt.Sprint(out[j]["type"], out[j]["name"], out[j]["data"], out[j]["priority"])
	})
	return out
}

func recordOutput(record Record) map[string]any {
	out := map[string]any{
		"id":        record.ID,
		"type":      strings.ToUpper(record.Type),
		"name":      record.Name,
		"data":      presentationTXTData(record.Type, record.Data),
		"ttl":       record.TTL,
		"priority":  record.Priority,
		"proxiable": record.Proxiable,
		"comment":   record.Comment,
	}
	if record.Proxied != nil {
		out["proxied"] = *record.Proxied
	}
	if len(record.Tags) > 0 {
		out["tags"] = append([]string(nil), record.Tags...)
	}
	return out
}

func recordsFromOutputs(outputs map[string]any) ([]Record, error) {
	if outputs == nil {
		return nil, nil
	}
	raw, ok := outputs["records"]
	if !ok || raw == nil {
		return nil, nil
	}
	var maps []map[string]any
	switch typed := raw.(type) {
	case []map[string]any:
		maps = typed
	case []any:
		maps = make([]map[string]any, 0, len(typed))
		for i, item := range typed {
			m, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("outputs.records[%d] must be an object", i)
			}
			maps = append(maps, m)
		}
	default:
		return nil, fmt.Errorf("outputs.records must be a list")
	}
	records := make([]Record, 0, len(maps))
	for _, m := range maps {
		record := Record{}
		record.ID, _ = m["id"].(string)
		record.Type, _ = m["type"].(string)
		record.Name, _ = m["name"].(string)
		record.Data, _ = m["data"].(string)
		record.Data = rawTXTData(record.Type, record.Data)
		record.Comment, _ = m["comment"].(string)
		record.TTL = intOutput(m["ttl"])
		record.Priority = intOutput(m["priority"])
		record.Proxiable, _ = m["proxiable"].(bool)
		if raw, ok := m["proxied"]; ok {
			if b, ok := raw.(bool); ok {
				record.Proxied = &b
			}
		}
		records = append(records, record)
	}
	return records, nil
}

func intOutput(raw any) int {
	switch v := raw.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}

type sdkClient struct {
	client *cloudflare.Client
}

func newSDKClient(apiToken string, requestTimeout time.Duration) *sdkClient {
	return newSDKClientWithOptions(apiToken, requestTimeout)
}

func newSDKClientWithOptions(apiToken string, requestTimeout time.Duration, opts ...option.RequestOption) *sdkClient {
	timeout := NormalizeRequestTimeout(requestTimeout)
	clientOpts := []option.RequestOption{
		option.WithAPIToken(apiToken),
		option.WithHTTPClient(&http.Client{Timeout: timeout}),
		option.WithMaxRetries(0),
	}
	clientOpts = append(clientOpts, opts...)
	return &sdkClient{client: cloudflare.NewClient(clientOpts...)}
}

func (c *sdkClient) GetZone(ctx context.Context, domain, zoneID string) (*Zone, error) {
	if zoneID != "" {
		zone, err := c.client.Zones.Get(ctx, zones.ZoneGetParams{ZoneID: cloudflare.String(zoneID)})
		if err != nil {
			return nil, err
		}
		return zoneFromSDK(zone), nil
	}
	iter := c.client.Zones.ListAutoPaging(ctx, zones.ZoneListParams{
		Name:    cloudflare.String(domain),
		PerPage: cloudflare.Float(50),
	})
	for iter.Next() {
		zone := iter.Current()
		if strings.EqualFold(zone.Name, domain) {
			return zoneFromSDK(&zone), nil
		}
	}
	if err := iter.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("zone %q not found", domain)
}

func (c *sdkClient) CreateZone(ctx context.Context, accountID, domain string) (*Zone, error) {
	zone, err := c.client.Zones.New(ctx, zones.ZoneNewParams{
		Account: cloudflare.F(zones.ZoneNewParamsAccount{ID: cloudflare.String(accountID)}),
		Name:    cloudflare.String(domain),
		Type:    cloudflare.F(zones.TypeFull),
	})
	if err != nil {
		return nil, err
	}
	return zoneFromSDK(zone), nil
}

func (c *sdkClient) DeleteZone(ctx context.Context, zoneID string) error {
	_, err := c.client.Zones.Delete(ctx, zones.ZoneDeleteParams{ZoneID: cloudflare.String(zoneID)})
	return err
}

func (c *sdkClient) ListRecords(ctx context.Context, zoneID string) ([]Record, error) {
	iter := c.client.DNS.Records.ListAutoPaging(ctx, cfdns.RecordListParams{
		ZoneID:  cloudflare.String(zoneID),
		PerPage: cloudflare.Float(500),
	})
	var out []Record
	for iter.Next() {
		record := iter.Current()
		out = append(out, recordFromSDK(&record))
	}
	if err := iter.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *sdkClient) CreateRecord(ctx context.Context, zoneID string, record Record) (*Record, error) {
	res, err := c.client.DNS.Records.New(ctx, cfdns.RecordNewParams{
		ZoneID: cloudflare.String(zoneID),
		Body:   newRecordBody(record),
	})
	if err != nil {
		return nil, err
	}
	out := recordFromSDK(res)
	return &out, nil
}

func (c *sdkClient) UpdateRecord(ctx context.Context, zoneID, recordID string, record Record) (*Record, error) {
	res, err := c.client.DNS.Records.Edit(ctx, recordID, cfdns.RecordEditParams{
		ZoneID: cloudflare.String(zoneID),
		Body:   editRecordBody(record),
	})
	if err != nil {
		return nil, err
	}
	out := recordFromSDK(res)
	return &out, nil
}

func (c *sdkClient) DeleteRecord(ctx context.Context, zoneID, recordID string) error {
	_, err := c.client.DNS.Records.Delete(ctx, recordID, cfdns.RecordDeleteParams{ZoneID: cloudflare.String(zoneID)})
	return err
}

func (c *sdkClient) GetDNSSEC(ctx context.Context, zoneID string) (*DNSSEC, error) {
	res, err := c.client.DNS.DNSSEC.Get(ctx, cfdns.DNSSECGetParams{ZoneID: cloudflare.String(zoneID)})
	if err != nil {
		return nil, err
	}
	return &DNSSEC{
		Status:          string(res.Status),
		DS:              res.DS,
		KeyTag:          int(res.KeyTag),
		Algorithm:       res.Algorithm,
		Digest:          res.Digest,
		DigestAlgorithm: res.DigestAlgorithm,
	}, nil
}

func zoneFromSDK(zone *zones.Zone) *Zone {
	if zone == nil {
		return nil
	}
	return &Zone{
		ID:                  zone.ID,
		Name:                zone.Name,
		Status:              string(zone.Status),
		NameServers:         append([]string(nil), zone.NameServers...),
		OriginalNameServers: append([]string(nil), zone.OriginalNameServers...),
		OriginalRegistrar:   zone.OriginalRegistrar,
		OriginalDNSHost:     zone.OriginalDnshost,
	}
}

func recordFromSDK(record *cfdns.RecordResponse) Record {
	if record == nil {
		return Record{}
	}
	proxied := record.Proxied
	tags := stringTags(record.Tags)
	return Record{
		ID:        record.ID,
		Type:      string(record.Type),
		Name:      record.Name,
		Data:      rawTXTData(string(record.Type), record.Content),
		TTL:       int(record.TTL),
		Priority:  int(record.Priority),
		Proxied:   &proxied,
		Proxiable: record.Proxiable,
		Comment:   record.Comment,
		Tags:      tags,
	}
}

func stringTags(raw any) []string {
	switch typed := raw.(type) {
	case []string:
		return append([]string(nil), typed...)
	default:
		return nil
	}
}

func newRecordBody(record Record) cfdns.RecordNewParamsBody {
	body := cfdns.RecordNewParamsBody{
		Name:    cloudflare.String(record.Name),
		TTL:     cloudflare.F(cfdns.TTL(record.TTL)),
		Type:    cloudflare.F(cfdns.RecordNewParamsBodyType(record.Type)),
		Content: cloudflare.String(rawTXTData(record.Type, record.Data)),
	}
	if record.Priority > 0 || record.Type == "MX" {
		body.Priority = cloudflare.Float(float64(record.Priority))
	}
	if record.Proxied != nil {
		body.Proxied = cloudflare.Bool(*record.Proxied)
	}
	if record.Comment != "" {
		body.Comment = cloudflare.String(record.Comment)
	}
	return body
}

func editRecordBody(record Record) cfdns.RecordEditParamsBody {
	body := cfdns.RecordEditParamsBody{
		Name:    cloudflare.String(record.Name),
		TTL:     cloudflare.F(cfdns.TTL(record.TTL)),
		Type:    cloudflare.F(cfdns.RecordEditParamsBodyType(record.Type)),
		Content: cloudflare.String(rawTXTData(record.Type, record.Data)),
	}
	if record.Priority > 0 || record.Type == "MX" {
		body.Priority = cloudflare.Float(float64(record.Priority))
	}
	if record.Proxied != nil {
		body.Proxied = cloudflare.Bool(*record.Proxied)
	}
	if record.Comment != "" {
		body.Comment = cloudflare.String(record.Comment)
	}
	return body
}

var _ = time.Time{}
