# Cloudflare DNS Portfolio Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add Cloudflare DNS import/apply support and improve DigitalOcean/Namecheap DNS import state for Workflow-managed domain migration.

**Architecture:** Implement a new `workflow-plugin-cloudflare` external IaC provider using `cloudflare-go/v7.3.0`, with `infra.dns` driver logic behind a testable client interface. Update existing provider-owned DNS outputs in DigitalOcean and Namecheap without moving provider logic into wfctl.

**Tech Stack:** Go 1.26, Workflow external plugin SDK, `github.com/cloudflare/cloudflare-go/v7 v7.3.0`, `github.com/digitalocean/godo`, `github.com/namecheap/go-namecheap-sdk/v2`.

**Base branch:** main

---

## Scope Manifest

**PR Count:** 3
**Tasks:** 6
**Estimated Lines of Change:** ~2200

**Out of scope:**
- Registrar transfer create/status workflows.
- Workflow Cloud or editor UI.
- wfctl-native provider bypass commands.
- Live migration of any real domain.

**PR Grouping:**

| PR # | Title | Tasks | Branch |
|------|-------|-------|--------|
| 1 | Add Cloudflare DNS IaC provider | Task 1, Task 2, Task 3, Task 4 | feat/cloudflare-dns-provider |
| 2 | Improve DigitalOcean DNS import outputs | Task 5 | feat/dns-import-outputs |
| 3 | Improve Namecheap DNS import/provider shim | Task 6 | feat/namecheap-dns-import |

**Status:** Locked 2026-05-23T00:00:00Z

## Task 1: Scaffold Cloudflare Workflow Plugin

**Files:**
- Create: `go.mod`
- Create: `.gitignore`
- Create: `.github/workflows/ci.yml`
- Create: `.goreleaser.yaml`
- Create: `LICENSE`
- Create: `README.md`
- Create: `plugin.json`
- Create: `cmd/workflow-plugin-cloudflare/main.go`
- Create: `internal/serve.go`

**Step 1: Write the minimal build/test surface**

Create the external plugin command and manifest modeled on `workflow-plugin-namecheap`, but with module type `iac.provider.cloudflare`, provider name `cloudflare`, and required secret `CLOUDFLARE_API_TOKEN`.

**Step 2: Run build**

Run: `GOWORK=off go build ./...`

Expected: build succeeds and emits no errors.

**Step 3: Commit**

Run: `git add . && git commit -m "feat: scaffold cloudflare plugin"`

Rollback: revert the commit and delete the unreleased plugin repo/branch.

## Task 2: Implement Cloudflare DNS Driver Tests

**Files:**
- Create: `internal/drivers/dns_test.go`
- Create: `internal/dns_test.go`

**Step 1: Write failing tests**

Cover:

- config validation requires API token.
- create creates missing zone when `account_id` is present.
- import/read lists all records and includes `name_servers`.
- diff detects proxied, TTL, MX priority, and record additions.
- update refuses undeclared-record deletion unless `manage_unlisted: true`.
- missing `records` errors before any mutating API call.

**Step 2: Run tests**

Run: `GOWORK=off go test ./internal/...`

Expected: FAIL because driver code is not implemented yet.

## Task 3: Implement Cloudflare DNS Driver

**Files:**
- Create: `internal/dns.go`
- Create: `internal/drivers/dns.go`

**Step 1: Implement config and adapter**

Add `Config`, validation, `cloudflareClient` interface, and a real SDK adapter using `cloudflare.NewClient(option.WithAPIToken(token))`.

**Step 2: Implement DNS resource behavior**

Support `Create`, `Read`, `Update`, `Delete`, `Diff`, `HealthCheck`, `SensitiveKeys`, and `ProviderIDFormat`. Use Cloudflare SDK v7 record and zone services in the real adapter.

**Step 3: Run tests**

Run: `GOWORK=off go test ./internal/...`

Expected: PASS.

**Step 4: Commit**

Run: `git add internal && git commit -m "feat: add cloudflare dns driver"`

Rollback: revert the commit and rebuild; no released config relies on this provider yet.

## Task 4: Implement Cloudflare IaC Server

**Files:**
- Create: `internal/iacserver.go`
- Create: `internal/iacserver_test.go`
- Modify: `internal/serve.go`
- Modify: `README.md`
- Modify: `plugin.json`

**Step 1: Write typed gRPC tests**

Cover name/version/capabilities, initialize missing token, import before initialize, import state payload, and Plan before initialize.

**Step 2: Implement server**

Mirror Namecheap's typed IaC provider server: `Name`, `Version`, `Capabilities`, `Initialize`, `Plan`, `Destroy`, `Status`, `Import`, `ResolveSizing`, `BootstrapStateBackend`, `FinalizeApply`, and provider shim.

**Step 3: Verify plugin load path**

Run: `GOWORK=off go test ./...`

Expected: PASS.

Run: `GOWORK=off go build -o /tmp/workflow-plugin-cloudflare ./cmd/workflow-plugin-cloudflare && /tmp/workflow-plugin-cloudflare --help`

Expected: process exits successfully or prints plugin server usage without panic.

**Step 4: Commit**

Run: `git add . && git commit -m "feat: expose cloudflare iac server"`

Rollback: revert the commit and rebuild the previous branch state.

## Task 5: Improve DigitalOcean DNS Import Outputs

**Files:**
- Modify: `internal/drivers/dns.go`
- Modify: `internal/drivers/dns_test.go`
- Modify: `internal/provider_test.go`
- Modify: `plugin.json`

**Step 1: Write failing tests**

Cover imported `infra.dns` outputs include `zone_file`, record IDs, and DigitalOcean nameserver guidance metadata without breaking existing `records` shape.

**Step 2: Implement additive outputs**

Add `zone_file`, `record_count`, `record.id`, and provider metadata such as `authoritative_nameservers` with DigitalOcean's canonical nameserver hostnames.

**Step 3: Verify**

Run: `GOWORK=off go test ./internal/...`

Expected: PASS.

**Step 4: Commit**

Run: `git add internal plugin.json && git commit -m "feat: enrich digitalocean dns import outputs"`

Rollback: revert the commit; existing configs are unaffected because outputs are additive.

## Task 6: Improve Namecheap DNS Import and Provider Shim

**Files:**
- Modify: `internal/drivers/dns.go`
- Modify: `internal/drivers/dns_test.go`
- Modify: `internal/iacserver.go`
- Modify: `internal/iacserver_test.go`
- Modify: `README.md`

**Step 1: Write failing tests**

Cover provider-shim `Import`, imported outputs include `is_using_our_dns`, `email_type`, and record count, and missing `records` still errors before zone replacement.

**Step 2: Implement additive import support**

Make `ncProvider.Import` call the DNS driver and return Workflow state. Preserve existing iacserver Import behavior and outputs.

**Step 3: Verify**

Run: `GOWORK=off go test ./...`

Expected: PASS.

**Step 4: Commit**

Run: `git add internal README.md && git commit -m "feat: improve namecheap dns import state"`

Rollback: revert the commit; existing configs are unaffected because behavior is additive except the provider shim now supports import.
