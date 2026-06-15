package drivers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/GoCodeAlone/workflow/interfaces"
	cloudflare "github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/rulesets"
)

const (
	redirectResourceType   = "infra.http_redirect"
	redirectRulesetName    = "Workflow managed redirect rules"
	redirectPhase          = "http_request_dynamic_redirect"
	redirectPermissionHint = "Cloudflare API token needs Zone > Single Redirect > Edit, or equivalent Dynamic URL Redirects Write access, to manage infra.http_redirect"
)

var nonRefChars = regexp.MustCompile(`[^a-z0-9]+`)

// RedirectClient is the testable Cloudflare zone/rulesets subset.
type RedirectClient interface {
	GetZone(ctx context.Context, domain, zoneID string) (*Zone, error)
	GetRedirectRuleset(ctx context.Context, zoneID string) (*RedirectRuleset, error)
	CreateRedirectRuleset(ctx context.Context, zoneID string, rules []RedirectRule) (*RedirectRuleset, error)
	UpdateRedirectRuleset(ctx context.Context, zoneID, rulesetID string, rules []RedirectRule) (*RedirectRuleset, error)
}

// RedirectRuleset is the zone phase entrypoint ruleset for Single Redirects.
type RedirectRuleset struct {
	ID     string
	ZoneID string
	Name   string
	Rules  []RedirectRule
}

// RedirectRule is the subset of Cloudflare redirect rule state this provider manages.
type RedirectRule struct {
	ID                  string
	Ref                 string
	Description         string
	Expression          string
	TargetURL           string
	StatusCode          int
	PreservePath        bool
	PreserveQueryString bool
	Enabled             bool
}

// RedirectDriver manages one Cloudflare Single Redirect rule in a zone ruleset.
type RedirectDriver struct {
	client           RedirectClient
	defaultAccountID string
	operationTimeout time.Duration
}

func NewRedirectDriver(apiToken string) *RedirectDriver {
	return NewRedirectDriverWithAccountRequestTimeout(apiToken, "", 0)
}

func NewRedirectDriverWithAccountRequestTimeout(apiToken, accountID string, requestTimeout time.Duration) *RedirectDriver {
	return &RedirectDriver{
		client:           newSDKClient(apiToken, NormalizeRequestTimeout(requestTimeout)),
		defaultAccountID: strings.TrimSpace(accountID),
		operationTimeout: defaultOperationTimeout,
	}
}

func NewRedirectDriverWithClient(client RedirectClient) *RedirectDriver {
	return &RedirectDriver{client: client, operationTimeout: defaultOperationTimeout}
}

func (d *RedirectDriver) operationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, NormalizeOperationTimeout(d.operationTimeout))
}

func (d *RedirectDriver) Create(ctx context.Context, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	ctx, cancel := d.operationContext(ctx)
	defer cancel()
	parsed, err := parseRedirectSpec(spec)
	if err != nil {
		return nil, fmt.Errorf("cloudflare redirect create %q: %w", spec.Name, err)
	}
	zone, err := d.client.GetZone(ctx, parsed.Domain, parsed.ZoneID)
	if err != nil {
		return nil, fmt.Errorf("cloudflare redirect create %q: lookup zone %q: %w", spec.Name, parsed.Domain, err)
	}
	ruleset, err := d.client.GetRedirectRuleset(ctx, zone.ID)
	if err != nil && !isCloudflareNotFound(err) {
		return nil, fmt.Errorf("cloudflare redirect create %q: read redirect ruleset: %w", spec.Name, redirectPermissionError(err))
	}
	rule := parsed.rule()
	if ruleset == nil || isCloudflareNotFound(err) {
		ruleset, err = d.client.CreateRedirectRuleset(ctx, zone.ID, []RedirectRule{rule})
	} else {
		ruleset, err = d.client.UpdateRedirectRuleset(ctx, zone.ID, ruleset.ID, upsertRedirectRule(ruleset.Rules, rule))
	}
	if err != nil {
		return nil, fmt.Errorf("cloudflare redirect create %q: %w", spec.Name, redirectPermissionError(err))
	}
	return redirectOutput(spec.Name, zone, ruleset, rule), nil
}

func (d *RedirectDriver) Read(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.ResourceOutput, error) {
	ctx, cancel := d.operationContext(ctx)
	defer cancel()
	domain, zoneID, refID := redirectRefParts(ref)
	if domain == "" && zoneID == "" {
		domain = ref.Name
	}
	zone, err := d.client.GetZone(ctx, domain, zoneID)
	if err != nil {
		return nil, fmt.Errorf("cloudflare redirect read %q: %w", ref.Name, err)
	}
	ruleset, err := d.client.GetRedirectRuleset(ctx, zone.ID)
	if err != nil {
		if isCloudflareNotFound(err) {
			return nil, fmt.Errorf("%w: cloudflare redirect read %q: %w", interfaces.ErrResourceNotFound, ref.Name, err)
		}
		return nil, fmt.Errorf("cloudflare redirect read %q: %w", ref.Name, redirectPermissionError(err))
	}
	if refID == "" {
		refID = redirectRefForHost(zone.Name)
	}
	rule, ok := findRedirectRule(ruleset.Rules, refID)
	if !ok {
		return nil, fmt.Errorf("%w: cloudflare redirect read %q: rule ref %q not found", interfaces.ErrResourceNotFound, ref.Name, refID)
	}
	return redirectOutput(ref.Name, zone, ruleset, rule), nil
}

func (d *RedirectDriver) Update(ctx context.Context, ref interfaces.ResourceRef, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	ctx, cancel := d.operationContext(ctx)
	defer cancel()
	parsed, err := parseRedirectSpec(spec)
	if err != nil {
		return nil, fmt.Errorf("cloudflare redirect update %q: %w", ref.Name, err)
	}
	zone, err := d.client.GetZone(ctx, parsed.Domain, parsed.ZoneID)
	if err != nil {
		return nil, fmt.Errorf("cloudflare redirect update %q: lookup zone %q: %w", ref.Name, parsed.Domain, err)
	}
	ruleset, err := d.client.GetRedirectRuleset(ctx, zone.ID)
	if err != nil {
		return nil, fmt.Errorf("cloudflare redirect update %q: read redirect ruleset: %w", ref.Name, redirectPermissionError(err))
	}
	rule := parsed.rule()
	ruleset, err = d.client.UpdateRedirectRuleset(ctx, zone.ID, ruleset.ID, upsertRedirectRule(ruleset.Rules, rule))
	if err != nil {
		return nil, fmt.Errorf("cloudflare redirect update %q: %w", ref.Name, redirectPermissionError(err))
	}
	return redirectOutput(spec.Name, zone, ruleset, rule), nil
}

func (d *RedirectDriver) Delete(ctx context.Context, ref interfaces.ResourceRef) error {
	ctx, cancel := d.operationContext(ctx)
	defer cancel()
	domain, zoneID, refID := redirectRefParts(ref)
	zone, err := d.client.GetZone(ctx, domain, zoneID)
	if err != nil {
		return fmt.Errorf("cloudflare redirect delete %q: %w", ref.Name, err)
	}
	ruleset, err := d.client.GetRedirectRuleset(ctx, zone.ID)
	if err != nil {
		return fmt.Errorf("cloudflare redirect delete %q: %w", ref.Name, redirectPermissionError(err))
	}
	if refID == "" {
		refID = redirectRefForHost(domain)
	}
	if refID == "" || refID == redirectRefForHost("") {
		return fmt.Errorf("cloudflare redirect delete %q: provider_id must include zone/ref when resource name is not a domain", ref.Name)
	}
	filtered := ruleset.Rules[:0]
	for _, rule := range ruleset.Rules {
		if rule.Ref != refID {
			filtered = append(filtered, rule)
		}
	}
	_, err = d.client.UpdateRedirectRuleset(ctx, zone.ID, ruleset.ID, filtered)
	if err != nil {
		return redirectPermissionError(err)
	}
	return nil
}

func (d *RedirectDriver) Diff(_ context.Context, desired interfaces.ResourceSpec, current *interfaces.ResourceOutput) (*interfaces.DiffResult, error) {
	if current == nil {
		return &interfaces.DiffResult{NeedsUpdate: true}, nil
	}
	parsed, err := parseRedirectSpec(desired)
	if err != nil {
		return nil, fmt.Errorf("cloudflare redirect diff: %w", err)
	}
	desiredOut := redirectRuleOutput(parsed.rule(), parsed.Domain, parsed.FromHost)
	changes := diffRedirectOutput(current.Outputs, desiredOut)
	return &interfaces.DiffResult{NeedsUpdate: len(changes) > 0, Changes: changes}, nil
}

func (d *RedirectDriver) HealthCheck(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.HealthResult, error) {
	out, err := d.Read(ctx, ref)
	if err != nil {
		return &interfaces.HealthResult{Healthy: false, Message: err.Error()}, nil
	}
	return &interfaces.HealthResult{Healthy: out.Status == "active", Message: out.Status}, nil
}

func (d *RedirectDriver) Scale(ctx context.Context, ref interfaces.ResourceRef, _ int) (*interfaces.ResourceOutput, error) {
	return d.Read(ctx, ref)
}

func (d *RedirectDriver) Type() string { return redirectResourceType }

func (d *RedirectDriver) SensitiveKeys() []string { return nil }

func redirectPermissionError(err error) error {
	if !isCloudflareAuthError(err) {
		return err
	}
	return fmt.Errorf("%s: %w", redirectPermissionHint, err)
}

func isCloudflareAuthError(err error) bool {
	var cfErr *cloudflare.Error
	if errors.As(err, &cfErr) && (cfErr.StatusCode == http.StatusUnauthorized || cfErr.StatusCode == http.StatusForbidden) {
		return true
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "403 forbidden") ||
		strings.Contains(lower, "401 unauthorized") ||
		strings.Contains(lower, `"code":10000`) ||
		strings.Contains(lower, "authentication error")
}

func (d *RedirectDriver) ProviderIDFormat() interfaces.ProviderIDFormat {
	return interfaces.IDFormatFreeform
}

type redirectSpec struct {
	Domain              string
	ZoneID              string
	FromHost            string
	TargetURL           string
	StatusCode          int
	PreservePath        bool
	PreserveQueryString bool
	Enabled             bool
	Ref                 string
	Description         string
}

func parseRedirectSpec(spec interfaces.ResourceSpec) (redirectSpec, error) {
	domain := redirectString(spec.Config, "domain", spec.Name)
	if domain == "" || !interfaces.ValidateProviderID(domain, interfaces.IDFormatDomainName) {
		return redirectSpec{}, fmt.Errorf("domain %q is not a valid domain name", domain)
	}
	fromHost := redirectString(spec.Config, "from_host", domain)
	if fromHost == "" || !interfaces.ValidateProviderID(fromHost, interfaces.IDFormatDomainName) {
		return redirectSpec{}, fmt.Errorf("from_host %q is not a valid domain name", fromHost)
	}
	target := redirectString(spec.Config, "target_url", "")
	if target == "" {
		return redirectSpec{}, fmt.Errorf("target_url is required")
	}
	parsedURL, err := url.Parse(target)
	if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		return redirectSpec{}, fmt.Errorf("target_url must be an absolute http(s) URL")
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return redirectSpec{}, fmt.Errorf("target_url scheme must be http or https")
	}
	status := redirectInt(spec.Config, "status_code", 301)
	switch status {
	case 301, 302, 303, 307, 308:
	default:
		return redirectSpec{}, fmt.Errorf("status_code must be one of 301, 302, 303, 307, 308")
	}
	preservePath := redirectBool(spec.Config, "preserve_path", true)
	preserve := redirectBool(spec.Config, "preserve_query_string", true)
	enabled := redirectBool(spec.Config, "enabled", true)
	ref := redirectString(spec.Config, "ref", redirectRefForHost(fromHost))
	description := redirectString(spec.Config, "description", fmt.Sprintf("Redirect %s to %s", fromHost, target))
	return redirectSpec{
		Domain:              strings.TrimSuffix(strings.ToLower(domain), "."),
		ZoneID:              redirectString(spec.Config, "zone_id", ""),
		FromHost:            strings.TrimSuffix(strings.ToLower(fromHost), "."),
		TargetURL:           target,
		StatusCode:          status,
		PreservePath:        preservePath,
		PreserveQueryString: preserve,
		Enabled:             enabled,
		Ref:                 ref,
		Description:         description,
	}, nil
}

func (s redirectSpec) rule() RedirectRule {
	return RedirectRule{
		Ref:                 s.Ref,
		Description:         s.Description,
		Expression:          fmt.Sprintf(`(http.host eq "%s")`, s.FromHost),
		TargetURL:           s.TargetURL,
		StatusCode:          s.StatusCode,
		PreservePath:        s.PreservePath,
		PreserveQueryString: s.PreserveQueryString,
		Enabled:             s.Enabled,
	}
}

func redirectString(m map[string]any, key, fallback string) string {
	if m == nil {
		return fallback
	}
	if v, ok := m[key].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return fallback
}

func redirectBool(m map[string]any, key string, fallback bool) bool {
	if m == nil {
		return fallback
	}
	if v, ok := m[key].(bool); ok {
		return v
	}
	return fallback
}

func redirectInt(m map[string]any, key string, fallback int) int {
	if m == nil {
		return fallback
	}
	switch v := m[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		if v == float64(int(v)) {
			return int(v)
		}
	}
	return fallback
}

func redirectRefForHost(host string) string {
	ref := nonRefChars.ReplaceAllString(strings.ToLower(strings.TrimSuffix(host, ".")), "_")
	ref = strings.Trim(ref, "_")
	if ref == "" {
		ref = "redirect"
	}
	return "workflow_redirect_" + ref
}

func upsertRedirectRule(rules []RedirectRule, desired RedirectRule) []RedirectRule {
	out := make([]RedirectRule, 0, len(rules)+1)
	replaced := false
	for _, rule := range rules {
		if rule.Ref == desired.Ref {
			out = append(out, desired)
			replaced = true
			continue
		}
		out = append(out, rule)
	}
	if !replaced {
		out = append(out, desired)
	}
	return out
}

func findRedirectRule(rules []RedirectRule, ref string) (RedirectRule, bool) {
	for _, rule := range rules {
		if rule.Ref == ref {
			return rule, true
		}
	}
	return RedirectRule{}, false
}

func redirectOutput(name string, zone *Zone, ruleset *RedirectRuleset, rule RedirectRule) *interfaces.ResourceOutput {
	outputs := redirectRuleOutput(rule, zone.Name, hostFromRedirectExpression(rule.Expression, zone.Name))
	outputs["zone_id"] = zone.ID
	outputs["ruleset_id"] = ruleset.ID
	return &interfaces.ResourceOutput{
		Name:       name,
		Type:       redirectResourceType,
		ProviderID: zone.ID + "/" + rule.Ref,
		Outputs:    outputs,
		Status:     "active",
	}
}

func redirectRuleOutput(rule RedirectRule, domain, fromHost string) map[string]any {
	return map[string]any{
		"domain":                domain,
		"from_host":             fromHost,
		"target_url":            rule.TargetURL,
		"status_code":           rule.StatusCode,
		"preserve_path":         rule.PreservePath,
		"preserve_query_string": rule.PreserveQueryString,
		"enabled":               rule.Enabled,
		"ref":                   rule.Ref,
		"expression":            rule.Expression,
	}
}

func hostFromRedirectExpression(expression, fallback string) string {
	const prefix = `(http.host eq "`
	const suffix = `")`
	if strings.HasPrefix(expression, prefix) && strings.HasSuffix(expression, suffix) {
		return strings.TrimSuffix(strings.TrimPrefix(expression, prefix), suffix)
	}
	return fallback
}

func diffRedirectOutput(current, desired map[string]any) []interfaces.FieldChange {
	paths := []string{"domain", "from_host", "target_url", "status_code", "preserve_path", "preserve_query_string", "enabled", "expression"}
	var changes []interfaces.FieldChange
	for _, path := range paths {
		if fmt.Sprint(current[path]) != fmt.Sprint(desired[path]) {
			changes = append(changes, interfaces.FieldChange{Path: path, Old: current[path], New: desired[path]})
		}
	}
	return changes
}

func redirectRefParts(ref interfaces.ResourceRef) (domain, zoneID, ruleRef string) {
	if ref.ProviderID != "" {
		parts := strings.SplitN(ref.ProviderID, "/", 2)
		zoneID = parts[0]
		if len(parts) == 2 {
			ruleRef = parts[1]
		}
	}
	if interfaces.ValidateProviderID(ref.Name, interfaces.IDFormatDomainName) {
		domain = ref.Name
	}
	return domain, zoneID, ruleRef
}

func (c *sdkClient) GetRedirectRuleset(ctx context.Context, zoneID string) (*RedirectRuleset, error) {
	res, err := c.client.Rulesets.Phases.Get(ctx, rulesets.Phase(redirectPhase), rulesets.PhaseGetParams{
		ZoneID: cloudflare.String(zoneID),
	})
	if err != nil {
		return nil, err
	}
	out := &RedirectRuleset{ID: res.ID, ZoneID: zoneID, Name: res.Name, Rules: make([]RedirectRule, 0, len(res.Rules))}
	for _, rule := range res.Rules {
		out.Rules = append(out.Rules, redirectRuleFromPhase(rule))
	}
	return out, nil
}

func (c *sdkClient) CreateRedirectRuleset(ctx context.Context, zoneID string, rules []RedirectRule) (*RedirectRuleset, error) {
	res, err := c.client.Rulesets.New(ctx, rulesets.RulesetNewParams{
		ZoneID:      cloudflare.String(zoneID),
		Name:        cloudflare.String(redirectRulesetName),
		Kind:        cloudflare.F(rulesets.Kind("zone")),
		Phase:       cloudflare.F(rulesets.Phase(redirectPhase)),
		Description: cloudflare.String("Workflow managed Cloudflare Single Redirect rules"),
		Rules:       cloudflare.F(newRedirectRuleParams(rules)),
	})
	if err != nil {
		return nil, err
	}
	out := &RedirectRuleset{ID: res.ID, ZoneID: zoneID, Name: res.Name, Rules: rules}
	return out, nil
}

func (c *sdkClient) UpdateRedirectRuleset(ctx context.Context, zoneID, rulesetID string, rules []RedirectRule) (*RedirectRuleset, error) {
	res, err := c.client.Rulesets.Phases.Update(ctx, rulesets.Phase(redirectPhase), rulesets.PhaseUpdateParams{
		ZoneID:      cloudflare.String(zoneID),
		Name:        cloudflare.String(redirectRulesetName),
		Description: cloudflare.String("Workflow managed Cloudflare Single Redirect rules"),
		Rules:       cloudflare.F(updateRedirectRuleParams(rules)),
	})
	if err != nil {
		return nil, err
	}
	id := rulesetID
	if res.ID != "" {
		id = res.ID
	}
	return &RedirectRuleset{ID: id, ZoneID: zoneID, Name: res.Name, Rules: rules}, nil
}

func redirectRuleFromPhase(rule rulesets.PhaseGetResponseRule) RedirectRule {
	out := RedirectRule{
		ID:          rule.ID,
		Ref:         rule.Ref,
		Description: rule.Description,
		Expression:  rule.Expression,
		Enabled:     rule.Enabled,
	}
	if rule.Ref == "" {
		out.Ref = rule.ID
	}
	if actionParams, ok := redirectActionParams(rule.ActionParameters); ok {
		out.TargetURL = actionParams.TargetURL
		out.StatusCode = actionParams.StatusCode
		out.PreservePath = actionParams.PreservePath
		out.PreserveQueryString = actionParams.PreserveQueryString
	}
	return out
}

type decodedRedirectActionParams struct {
	FromValue struct {
		TargetURL struct {
			Value      string `json:"value"`
			Expression string `json:"expression"`
		} `json:"target_url"`
		PreserveQueryString bool `json:"preserve_query_string"`
		StatusCode          int  `json:"status_code"`
	} `json:"from_value"`
}

func redirectActionParams(raw any) (RedirectRule, bool) {
	data, err := json.Marshal(raw)
	if err != nil {
		return RedirectRule{}, false
	}
	var decoded decodedRedirectActionParams
	if err := json.Unmarshal(data, &decoded); err != nil {
		return RedirectRule{}, false
	}
	target := decoded.FromValue.TargetURL.Value
	preservePath := false
	if target == "" {
		target = decoded.FromValue.TargetURL.Expression
		if parsed, ok := targetFromPreservePathExpression(target); ok {
			target = parsed
			preservePath = true
		}
	}
	return RedirectRule{
		TargetURL:           target,
		StatusCode:          decoded.FromValue.StatusCode,
		PreservePath:        preservePath,
		PreserveQueryString: decoded.FromValue.PreserveQueryString,
	}, target != ""
}

func targetFromPreservePathExpression(expression string) (string, bool) {
	const prefix = `concat(`
	const suffix = `, http.request.uri.path)`
	if !strings.HasPrefix(expression, prefix) || !strings.HasSuffix(expression, suffix) {
		return "", false
	}
	quoted := strings.TrimSuffix(strings.TrimPrefix(expression, prefix), suffix)
	target, err := strconv.Unquote(quoted)
	if err != nil {
		return "", false
	}
	return target, true
}

func newRedirectRuleParams(rules []RedirectRule) []rulesets.RulesetNewParamsRuleUnion {
	out := make([]rulesets.RulesetNewParamsRuleUnion, 0, len(rules))
	for _, rule := range rules {
		out = append(out, redirectRuleParam(rule))
	}
	return out
}

func updateRedirectRuleParams(rules []RedirectRule) []rulesets.PhaseUpdateParamsRuleUnion {
	out := make([]rulesets.PhaseUpdateParamsRuleUnion, 0, len(rules))
	for _, rule := range rules {
		out = append(out, redirectRuleParam(rule))
	}
	return out
}

func redirectRuleParam(rule RedirectRule) rulesets.RedirectRuleParam {
	targetURL := rulesets.RedirectRuleActionParametersFromValueTargetURLParam{}
	if rule.PreservePath {
		targetURL.Expression = cloudflare.String(redirectTargetExpression(rule.TargetURL))
	} else {
		targetURL.Value = cloudflare.String(rule.TargetURL)
	}
	param := rulesets.RedirectRuleParam{
		Ref:         cloudflare.String(rule.Ref),
		Description: cloudflare.String(rule.Description),
		Expression:  cloudflare.String(rule.Expression),
		Enabled:     cloudflare.Bool(rule.Enabled),
		Action:      cloudflare.F(rulesets.RedirectRuleActionRedirect),
		ActionParameters: cloudflare.F(rulesets.RedirectRuleActionParametersParam{
			FromValue: cloudflare.F(rulesets.RedirectRuleActionParametersFromValueParam{
				TargetURL:           cloudflare.F(targetURL),
				StatusCode:          cloudflare.F(rulesets.RedirectRuleActionParametersFromValueStatusCode(rule.StatusCode)),
				PreserveQueryString: cloudflare.Bool(rule.PreserveQueryString),
			}),
		}),
	}
	if rule.ID != "" {
		param.ID = cloudflare.String(rule.ID)
	}
	return param
}

func redirectTargetExpression(targetURL string) string {
	return fmt.Sprintf("concat(%s, http.request.uri.path)", strconv.Quote(targetURL))
}
