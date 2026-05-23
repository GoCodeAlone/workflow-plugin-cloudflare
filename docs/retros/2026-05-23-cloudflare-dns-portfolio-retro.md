# Retro: Cloudflare DNS Portfolio

**PR:** #1 - feat: add Cloudflare DNS IaC provider
**Merged:** 2026-05-23
**Branch:** feat/cloudflare-dns-provider
**Design:** docs/plans/2026-05-23-cloudflare-dns-portfolio-design.md
**Plan:** docs/plans/2026-05-23-cloudflare-dns-portfolio.md
**Related ADRs:** none

## Adversarial-review findings, scored

| Phase | Finding | Severity | Outcome |
|---|---|---|---|
| design | Full-zone apply could delete live records unexpectedly. | Critical | Resolved upfront |
| design | Token scope and secret handling needed explicit documentation. | Important | Resolved upfront |
| design | Registrar transfer automation should not be promised in first provider PR. | Important | Resolved upfront |
| plan | Import state must preserve authority metadata and record details. | Important | Resolved upfront |
| plan | Cloudflare SDK v7 generated types could leak into tests and driver logic. | Important | Resolved upfront |
| plan | The provider needed a realistic plugin server, not only a driver. | Important | Resolved upfront |

## Gate misses

No gate misses this PR. The missing domain-provider-ID behavior and omitted-`proxied` diff behavior were caught by the inline requesting-code-review pass before merge, then fixed in the PR branch. CI stayed green across the PR and merge commit.

| Issue | Gate that missed | Why it slipped | Fix idea |
|---|---|---|---|
| None | n/a | n/a | n/a |

## Missed skill activations

| Gate | Fired? | Notes |
|---|---|---|
| brainstorming | yes | Captured provider-first DNS import and migration scope. |
| adversarial-design-review (design) | yes | Forced non-destructive DNS defaults and token-scope documentation. |
| writing-plans | yes | Split Cloudflare, DigitalOcean, and Namecheap PRs. |
| adversarial-design-review (plan) | yes | Tightened import/state requirements before implementation. |
| alignment-check | yes | Confirmed plan coverage before execution. |
| scope-lock | yes | Locked the three-PR scope. |
| requesting-code-review | yes | Ran inline due Codex subagent restrictions; found two Cloudflare edge cases. |
| pr-monitoring | yes | Monitored PR and post-merge CI to green. |
| post-merge-retrospective | yes | This file. |

## What worked

- Design review prevented destructive DNS behavior by making undeclared-record deletion opt-in with `manage_unlisted`.
- The SDK adapter boundary kept `cloudflare-go/v7.3.0` details out of most tests.
- Inline code review caught two behavior bugs that CI would not have found from happy-path tests alone.
- Post-merge monitoring caught the long-running race-test jobs without merging early.

## What didn't

- The first implementation still needed a late review pass to catch provider ID interpretation for imports.
- Proxied-state ownership semantics were under-specified until code review.

## Plugin-level follow-ups

No plugin-level superpowers changes are warranted from a single PR. Future DNS provider plans should explicitly include provider-ID normalization and tri-state ownership for provider-specific optional fields.
