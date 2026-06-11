// Package internal implements workflow-plugin-cloudflare, an external Workflow
// IaC provider for Cloudflare DNS zones and Cloudflare Registrar domains.
//
// The provider exposes the `iac.provider.cloudflare` module and the
// `infra.dns` and `infra.domain` resource types. It is intended to run through
// Workflow's external plugin host; Go consumers normally configure it in a
// Workflow manifest rather than importing the package directly.
//
// Cloudflare authentication is supplied through the `api_token` provider
// configuration field, commonly sourced from the `CLOUDFLARE_API_TOKEN` secret.
// The plugin uses github.com/cloudflare/cloudflare-go/v7 for all live
// Cloudflare API calls. Tests should prefer NewDNSDriverWithClient and
// NewDomainDriverWithClient in the drivers package so they can exercise provider
// behavior without live Cloudflare credentials.
//
// Account identity is not a secret. Use the `account_id` provider or resource
// configuration field when creating missing zones or when managing
// `infra.domain`, because Cloudflare's zone-creation and registrar APIs are
// account-scoped. Existing-zone imports and updates may use a zone ID or domain
// name and therefore do not always need `account_id`.
//
// `infra.dns` preserves live DNS records that are not declared in the desired
// config unless `manage_unlisted` is set to true. This makes unattended apply
// paths safer for verification records, mail records, and registrar-required
// Cloudflare nameserver state. `infra.domain` is import-first: it can read
// registrar metadata and optionally update auto-renew when explicitly allowed,
// but it does not buy, transfer, or delete domains.
package internal
