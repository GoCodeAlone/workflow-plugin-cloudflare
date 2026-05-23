# Cloudflare DNS Portfolio Adversarial Design Review

### Adversarial Review Report

**Phase:** design
**Artifact:** `docs/plans/2026-05-23-cloudflare-dns-portfolio-design.md`
**Status:** PASS

**Findings (Critical):**
- None.

**Findings (Important):**
- [Missing failure modes] The design originally risked whole-zone delete behavior by treating one resource as full-zone state. Recommendation: default Cloudflare apply to non-destructive upsert and require `manage_unlisted: true` before removing undeclared live records. Status: addressed in the design.
- [User-intent drift] Registrar transfer automation is part of the broader user goal, but implementing it before DNS import/apply would increase blast radius. Recommendation: explicitly call registrar transfer out of scope for this first pass and preserve API-backed expansion later. Status: addressed in the design.
- [Repo-precedent conflicts] DigitalOcean uses provider-owned DNS logic and Namecheap uses a typed IaC server. A central wfctl command would fight the workspace guide's plugin-first direction. Recommendation: keep all provider behavior inside `workflow-plugin-*` repos. Status: addressed in the design.

**Findings (Minor):**
- [YAGNI] A dedicated UI safety model is discussed but not implemented. Recommendation: make plugin outputs UI-friendly, but do not add UI code in this pass.
- [Rollback story] SDK pin rollback needs to be explicit because a new generated SDK can break builds. Recommendation: document revert-to-previous-dependency rollback. Status: addressed.
- [Security] Cloudflare tokens need least-privilege guidance. Recommendation: README and plugin metadata should describe required token scopes and keep secrets declarative.

**Bug-class scan transcript:**

| Class | Result | Note |
|---|---|---|
| Unstated assumptions | Clean | The design lists SDK version, token scope, authoritative DNS, state shape, and plugin-first assumptions. |
| Repo-precedent conflicts | Finding | Central wfctl orchestration would violate provider ownership; design uses provider plugins instead. |
| YAGNI violations | Finding | UI and registrar migration are acknowledged but deferred from implementation. |
| Missing failure modes | Finding | Partial DNS apply and accidental zone wipe were identified; `manage_unlisted` default-safe behavior addresses them. |
| Security / privacy | Finding | Token scope and secret handling must be documented in plugin metadata and README. |
| Rollback story | Clean | New plugin rollback and additive provider rollback are defined. |
| Simpler alternative not considered | Clean | One-off export script is considered and rejected because it bypasses Workflow state/import contracts. |
| User-intent drift | Finding | Registrar migration is intentionally deferred to keep this pass focused on safe DNS import and state. |

**Options the author may not have considered:**
1. Use Terraform/OpenTofu as the only DNS state layer. This would reduce custom provider code but would not improve Workflow plugin import coverage or plugin-first UI/wfctl integration.
2. Implement Cloudflare-only migration first. This is faster for one portfolio, but leaves DO/Namecheap imports weaker and conflicts with the user's request to invest in reusable provider coverage.

**Verdict reasoning:** PASS because no Critical findings remain and the Important findings are addressed in the design by narrowing the first pass to plugin-owned import/state/apply coverage, defaulting DNS deletion to safe behavior, and documenting rollback.
