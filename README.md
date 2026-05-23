# workflow-plugin-cloudflare

Cloudflare DNS provider for the GoCodeAlone/workflow IaC surface.
Implements `infra.dns` using the official
[`cloudflare-go/v7`](https://github.com/cloudflare/cloudflare-go) SDK.

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

## Required secrets

| Name | Sensitive | Source |
|------|-----------|--------|
| `CLOUDFLARE_API_TOKEN` | yes | Cloudflare API token |

For existing-zone import and DNS management, use a token with Zone:Read and
DNS:Edit scoped to the relevant zones. Creating a missing zone also requires
permission to create zones in the target account.

## Import

`wfctl` can import an existing Cloudflare zone by provider ID. The provider ID
may be a zone ID or the domain name. Imported outputs include zone nameservers,
original nameservers/registrar metadata, DNSSEC status when readable, and all
DNS records returned by the Cloudflare API.

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
