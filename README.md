# workflow-plugin-cloudflare

Cloudflare DNS, redirect, and Registrar provider for the GoCodeAlone/workflow IaC surface.
Implements `infra.dns`, `infra.http_redirect`, and `infra.domain` using the official
[`cloudflare-go/v7`](https://github.com/cloudflare/cloudflare-go) SDK.
`infra.domain` is import-first and covers Cloudflare Registrar metadata plus
guarded auto-renew management.

One `infra.dns` resource manages one Cloudflare zone. Create and update
operations upsert declared records by default. Records that exist in
Cloudflare but are not declared are preserved unless `manage_unlisted: true`
is set.

## Configuration

```yaml
modules:
  - name: cloudflare
    type: iac.provider.cloudflare
    config:
      api_token: ${CLOUDFLARE_API_TOKEN}
      # account_id is non-secret configuration. It is required for creating
      # missing zones and for infra.domain registrar operations.
      account_id: ${CLOUDFLARE_ACCOUNT_ID}
      # Optional Go duration for Cloudflare API calls. Defaults to 30s.
      request_timeout: 30s

resources:
  - name: example-com
    type: infra.dns
    config:
      provider: cloudflare
      account_id: ${CLOUDFLARE_ACCOUNT_ID}
      domain: example.com
      records:
        - { type: A, name: "@", data: 203.0.113.10, ttl: 300, proxied: true }
        - { type: CNAME, name: www, data: example.com, ttl: 300, proxied: true }
        - { type: MX, name: "@", data: aspmx.l.google.com, ttl: 300, priority: 1 }
        - { type: TXT, name: "@", data: "v=spf1 include:_spf.google.com ~all", ttl: 300 }
```

## HTTP Redirects

`infra.http_redirect` manages one Cloudflare Single Redirect rule in a zone's
`http_request_dynamic_redirect` entrypoint ruleset. It preserves unrelated
rules in the same ruleset and uses a stable rule `ref` so updates do not create
duplicate rules.

Cloudflare only evaluates redirect rules for hostnames proxied through
Cloudflare. Redirect-only domains should also declare an originless proxied DNS
record, such as an `A` record for `@` pointing at `192.0.2.1`.

```yaml
resources:
  - name: example-net
    type: infra.dns
    config:
      provider: cloudflare
      domain: example.net
      manage_unlisted: true
      records:
        - { type: A, name: "@", data: 192.0.2.1, ttl: 1, proxied: true }

  - name: example-net-redirect
    type: infra.http_redirect
    config:
      provider: cloudflare
      domain: example.net
      from_host: example.net
      target_url: https://example.com
      status_code: 301
      preserve_path: true
      preserve_query_string: true
```

## Registrar Domains

`infra.domain` manages existing Cloudflare Registrar domains after import. It
does not purchase, transfer, or delete domains. This keeps registrar billing and
domain ownership changes out of unattended IaC apply paths.

```yaml
modules:
  - name: cloudflare
    type: iac.provider.cloudflare
    config:
      api_token: ${CLOUDFLARE_API_TOKEN}
      account_id: ${CLOUDFLARE_ACCOUNT_ID}

resources:
  - name: example-com-domain
    type: infra.domain
    config:
      provider: cloudflare
      domain: example.com
      auto_renew: true
      # Required before apply may change auto_renew.
      allow_auto_renew_update: true
```

Import existing Cloudflare Registrar domains with provider ID equal to the
domain name. Imported outputs include registration status, lock state, privacy
mode, expiration, and Cloudflare async workflow snapshots when readable.

## Required secrets

| Name | Sensitive | Source |
|------|-----------|--------|
| `CLOUDFLARE_API_TOKEN` | yes | Cloudflare API token |

## Required configuration

| Name | Sensitive | Source |
|------|-----------|--------|
| `CLOUDFLARE_ACCOUNT_ID` | no | Cloudflare account ID |

`CLOUDFLARE_ACCOUNT_ID` is provider configuration, not credential material. Set
it on the `iac.provider.cloudflare` module when a workflow creates missing zones
or uses `infra.domain`. You may also set `account_id` per `infra.dns` resource
when different zones live in different Cloudflare accounts.

Cloudflare API calls are bounded by a 30 second timeout by default. Override
with provider config `request_timeout` or the `CLOUDFLARE_REQUEST_TIMEOUT`
environment variable using Go duration syntax, such as `10s` or `1m`.

```sh
wfctl secrets setup --plugin workflow-plugin-cloudflare
wfctl vars setup --plugin workflow-plugin-cloudflare
```

For existing-zone import and DNS management, use a token with Zone:Read and
DNS:Edit scoped to the relevant zones. Creating a missing zone also requires
permission to create zones in the target account. `infra.http_redirect` requires
Dynamic URL Redirects Write for the target zone. Registrar import/status needs
Registrar read access; changing `auto_renew` needs Registrar edit access.

## Import

`wfctl` can import an existing Cloudflare zone by provider ID. The provider ID
may be a zone ID or the domain name. Imported outputs include zone nameservers,
original nameservers/registrar metadata, DNSSEC status when readable, and all
DNS records returned by the Cloudflare API.

For `infra.domain`, the provider ID is the domain name and `account_id` must be
configured on the provider module because Cloudflare Registrar APIs are
account-scoped.

## Go Integration Notes

The runtime entrypoint is `cmd/workflow-plugin-cloudflare`, which serves
`internal.NewIaCServer` through Workflow's external plugin host. Application code
usually references this plugin from a Workflow manifest with
`iac.provider.cloudflare`; direct Go imports are mainly useful for provider
tests.

The production implementation uses `github.com/cloudflare/cloudflare-go/v7`.
Tests should use the driver constructors that accept narrow interfaces:
`drivers.NewDNSDriverWithClient` for zone and record behavior, and
`drivers.NewDomainDriverWithClient` for Cloudflare Registrar behavior. Redirect
tests use `drivers.NewRedirectDriverWithClient`. That keeps tests deterministic
and avoids requiring live Cloudflare credentials.

`infra.dns` can read or update existing zones by provider ID. Creating a missing
zone requires `account_id` because Cloudflare zone creation is account-scoped.
`infra.domain` always requires `account_id`, because Cloudflare Registrar APIs
are account-scoped.

## Deletion Safety

Cloudflare is often the registrar's mandatory authoritative DNS provider. To
avoid accidental mail or verification-record loss, undeclared live records are
not removed unless `manage_unlisted: true` is set on the resource. A missing
`records` key is an error; an explicit `records: []` is accepted as intentional
empty desired state.

## Development

```sh
GOWORK=off go build ./...
GOWORK=off go test ./... -race -count=1
```
