# Cloudflare DNS Portfolio Design

## Context

The DNS portfolio workflow should let users import existing domains and records into Workflow IaC, then migrate authority and registrar ownership deliberately. Cloudflare is the preferred authoritative DNS target when Cloudflare Registrar is used, while DigitalOcean and Namecheap remain provider-owned integrations for origins, alternate DNS authority, and registrar paths that should not be coupled into wfctl itself.

Cloudflare's current Go SDK line is `github.com/cloudflare/cloudflare-go/v7`; this design pins `v7.3.0`, released 2026-05-20. The plugin must use that SDK for live Cloudflare calls rather than a hand-rolled REST client.

## Goals

- Add `workflow-plugin-cloudflare` as an external Workflow IaC provider for `infra.dns`.
- Support importing Cloudflare zones and full DNS record state into Workflow state.
- Preserve high-risk DNS records such as MX, SPF/DKIM/DMARC TXT, verification TXT, CAA, NS, SRV, and apex records during import.
- Keep DigitalOcean and Namecheap DNS drivers aligned with the same import/state management goals.
- Keep provider behavior plugin-first so wfctl and future UI surfaces consume provider capabilities instead of bypassing plugins.

## Non-Goals

- No UI is built in this pass. The plugin outputs and validation are shaped so a later UI can front plans safely.
- No registrar transfer automation is implemented in the first Cloudflare PR. Registrar status can be added after DNS import and apply behavior is stable.
- No generic multi-provider domain portfolio orchestrator is added to wfctl.

## Architecture

`workflow-plugin-cloudflare` follows the current external IaC provider pattern used by `workflow-plugin-namecheap`: a typed gRPC IaC server, a provider shim for `platform.ComputePlan`, and an `infra.dns` driver. The real client adapter wraps `cloudflare-go/v7` services, while unit tests inject a small interface.

One `infra.dns` resource manages one Cloudflare zone. The resource config accepts:

- `zone_id`, optional for already-known Cloudflare zones.
- `domain`, required unless the resource name is the zone apex.
- `records`, required for create/update and explicit full-zone desired state.
- `account_id`, required only when the plugin must create a missing Cloudflare zone.
- per-record fields: `type`, `name`, `data`, `ttl`, `priority`, `proxied`, `comment`, and CAA/SRV support fields.

The driver reads the Cloudflare zone, lists all DNS records through SDK autopaging, and produces stable state outputs:

- zone identity: `zone_id`, `domain`, `status`, `name_servers`, `original_name_servers`, `original_registrar`, `original_dnshost`.
- DNSSEC snapshot when available.
- `records` as a JSON-safe list of maps with record IDs, types, names, values, TTL, priority, proxied/proxiable, comments, tags, and provider settings.

Cloudflare DNS updates are record-level because Cloudflare has per-record create/update/delete APIs. The first implementation uses conservative "upsert declared records, delete unmanaged records only when `manage_unlisted: true`" semantics. Import uses read-only state capture and never mutates.

DigitalOcean and Namecheap remain separate plugins. DO gets better import outputs for zone file and DO nameserver intent. Namecheap gets provider-shim import support and richer imported metadata for `is_using_our_dns` and `email_type`, so import flows can detect when Namecheap DNS is not actually authoritative.

## Safety Model

The safe migration sequence is:

1. Import current authoritative provider state.
2. Generate desired `infra.dns` config from imported state.
3. Plan until the provider reports no drift.
4. Create Cloudflare zone and records.
5. Validate external DNS/MX/TXT/CAA before changing nameservers.
6. Change nameservers or begin registrar transfer outside this plugin pass.

Apply refuses implicit zone wipes. Missing `records` is an error. An empty `records: []` is allowed only as an explicit desired state. Destructive unlisted-record deletion requires `manage_unlisted: true`, so a partial app config cannot accidentally delete MX or verification records.

## Rollback

The Cloudflare plugin is a new external provider. Rollback is to uninstall or stop selecting `iac.provider.cloudflare` and continue using existing provider plugins. For DNS record applies, rollback is to re-apply the prior Workflow state snapshot or import snapshot through the same provider. For SDK pin regressions, rollback is to revert the dependency change and rebuild the plugin binary.

DigitalOcean and Namecheap changes are additive to outputs/import behavior. Rollback is to revert their plugin PRs; existing resource configs remain compatible because no fields are removed.

## Assumptions

- Cloudflare API tokens have Zone:Read and DNS:Edit for managed zones; zone creation additionally requires Account/Zone creation permission.
- Cloudflare zones intended for Cloudflare Registrar are full zones using Cloudflare authoritative nameservers.
- Workflow state can preserve JSON-safe nested lists in `OutputsJson`.
- Provider-specific DNS fields are acceptable in `infra.dns` config as optional extensions.
- Users will not rely on wfctl bypassing provider plugins for DNS migration.

## Approaches Considered

1. **Recommended: provider-first DNS import and apply.** Build Cloudflare, harden DO/Namecheap outputs, leave orchestration/UI for a later pass. This matches Workflow ownership boundaries and gives immediate import/state value.
2. **wfctl domain portfolio command first.** Faster UX, but it would centralize provider assumptions in wfctl and likely bypass plugin capability negotiation.
3. **Cloudflare-only consolidation.** Useful for Jon's portfolio but leaves future users with weaker migration paths from Namecheap and DO.

## Self-Challenge

- The laziest solution is a one-off script that exports records from provider APIs into YAML. That would not exercise Workflow plugin contracts or state import, so it does not satisfy future plugin reuse.
- The fragile assumption is that `infra.dns` can carry provider-specific fields without confusing other providers. The design confines those fields to provider plugins and outputs, not wfctl core.
- Partial failure first appears during record apply after some records were changed. The design avoids registrar/nameserver changes in the same pass and keeps import snapshots as rollback inputs.

## Top Doubts

- Cloudflare SDK v7 is generated and broad; its typed DNS unions may be more cumbersome than the old SDK. The plan should isolate this in an adapter interface and keep tests independent of SDK internals.
- A full-zone delete mode is dangerous. The first implementation should default to non-destructive upsert and require `manage_unlisted: true` before deleting undeclared records.
- Registrar transfer APIs are tempting but not needed for first safe migration. Adding them before DNS import/apply is stable would expand blast radius.
