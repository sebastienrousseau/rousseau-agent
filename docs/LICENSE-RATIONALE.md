# Licensing rationale — proposal, pending decision

**Status: PROPOSAL, 2026-09-06 — not yet adopted.** The core is
currently dual-licensed Apache-2.0 OR MIT (see `LICENSE-APACHE`
and `LICENSE-MIT`). This document lays out the case for tightening
that to a Fair Source / delayed-open-source license before there
is enough distribution to make the migration painful. **Do not
change `LICENSE` / `LICENSE-APACHE` / `LICENSE-MIT` without
explicit sign-off from the copyright holder.**

## Current state (2026-09-06)

- Core: Apache-2.0 OR MIT, at the user's option.
- Copyright: Sebastien Rousseau.
- Contributor Licence Agreement: **none in place**.
- Enterprise Edition surfaces (SSO, audit egress, RBAC + OPA) are
  runtime-gated by an offline Ed25519-signed license key. The
  Enterprise Edition binary IS the same binary as Community —
  features are toggled by the license, not compiled in / out.

## The risk

Apache-2.0 and MIT are unrestricted permissive licences. They
permit any third party — including AWS, GCP, Azure, Cloudflare,
and future hyperscalers — to fork the core, wrap it in a hosted
managed service, and out-distribute the maintainer under the
same name or a rebranded one. This is exactly the scenario that
forced HashiCorp (Terraform / Vault / Vagrant, Apache-2.0 →
BUSL v1.1 in August 2023), Redis Labs (BSD → RSAL / SSPL),
MongoDB (AGPL → SSPL), CockroachDB (Apache-2.0 → BSL), Elastic
(Apache-2.0 → Elastic License + SSPL), and Sentry (BSL → FSL)
to relicense under commercial-competitive pressure.

Relicensing under pressure is expensive: contributor complaints,
distribution-channel disruption, community fork risk (OpenSearch
from Elasticsearch, OpenTofu from Terraform). Relicensing before
you have enough distribution to lose is cheap and forward-looking.

**rousseau-agent is presently at 1 GitHub star and a solo
contributor tree.** The migration cost today is a `git mv` and a
CHANGELOG entry. In two years, if the enterprise-tier trajectory
holds, it would be a public governance event.

## Options considered

### Option A — Keep Apache-2.0 OR MIT

Do nothing.

- **Pro:** Zero effort. Maximum ecosystem compatibility. OSI-approved,
  Debian-blessed, unambiguous.
- **Con:** Any hyperscaler can wrap the core, monetise it, and
  outcompete the maintainer on distribution. Enterprise-tier
  features are the only defence, and the defence relies on the
  Enterprise Edition being valuable enough that customers pay
  rather than fork.
- **Con:** Contributor licence assignment is ambiguous — future
  relicensing would require re-signing every non-trivial
  contributor.

### Option B — Functional Source License (FSL-1.1-Apache-2.0)

Adopt Sentry's FSL. Two-year commercial-use restriction, then
automatic transition to Apache-2.0.

- **Pro:** Blocks the hyperscaler-fork attack for two years, after
  which each version becomes Apache-2.0 anyway — user gets both
  short-term protection and long-term open-source guarantee.
- **Pro:** OSI has not certified FSL, but it is widely accepted in
  the "fair-source" ecosystem (Sentry, Codecrafters,
  agree.com). Fossa treats it as Fair Source. TechCrunch coverage
  legitimises it in the developer press.
- **Pro:** The Apache-2.0 transition preserves compatibility with
  the existing dual-licence — anyone using the current code under
  Apache-2.0 continues to do so; the FSL restriction only bites
  for the two-year competing-commercial-use window on each new
  version.
- **Con:** Not OSI-approved. Some Linux distributions (Fedora,
  Debian) may exclude non-OSI-certified code from main repositories
  during the FSL two-year window.
- **Con:** Contributors need to agree to the new licence via a
  CLA (see Option D).

### Option C — Business Source License (BUSL v1.1)

Adopt HashiCorp / Sentry's original BUSL. Four-year restriction,
then MPL-2.0.

- **Pro:** Better-known than FSL; HashiCorp's use lends
  legitimacy.
- **Con:** Longer restriction window (four years vs. FSL's two).
- **Con:** MPL-2.0 fallback is less compatible with downstream
  Apache-2.0 use than an Apache-2.0 fallback.

### Option D — Add a Contributor Licence Agreement, keep Apache-2.0/MIT

Adopt a CLA (e.g. the [Developer Certificate of Origin](https://developercertificate.org)
plus a rights assignment) without relicensing.

- **Pro:** Minimum-viable protection. Preserves the option to
  relicense later without re-signing every contributor.
- **Pro:** Compatible with all downstream OSS use today.
- **Con:** Does not solve the hyperscaler-fork problem — it just
  reserves the RIGHT to solve it later.
- **Recommend regardless of Option A/B/C:** having a CLA is
  strictly better than not having one, and it costs one PR to add.

## Recommendation

**Option B (FSL-1.1-Apache-2.0) for the core + Option D (CLA) for
all contributions.** Rationale:

1. The Enterprise Edition business model depends on the maintainer
   remaining the vendor of record. FSL blocks the hyperscaler-fork
   attack that would otherwise undermine that.
2. The Apache-2.0 fallback after two years preserves the open-
   source guarantee — this is not a rug-pull; it is a delay.
3. At current distribution (1 star, 1 contributor), the migration
   cost is nil. In eighteen months, it may be political.
4. Every fair-source precedent (Sentry, HashiCorp) has vindicated
   the choice — none of them have lost meaningful community
   goodwill over it, and all of them protected the commercial
   trajectory that funds the maintenance.

## What NOT to do

- Do not adopt SSPL (MongoDB / Elastic). Fedora and Debian both
  reject SSPL. The community sentiment shift on SSPL specifically
  has been negative, and rousseau-agent has no name recognition
  to spend on a controversial licence.
- Do not adopt AGPL. It is OSI-approved but hostile to enterprise
  buyers — every enterprise procurement team maintains an "AGPL
  banned" list. This directly conflicts with the target buyer.
- Do not adopt a proprietary source-available licence. It gains
  the maintainer no protection Elastic / BUSL / FSL doesn't already
  provide, and burns all open-source community goodwill.

## Migration plan (if Option B is chosen)

1. **Add CLA first.** Set up EasyCLA or CLA-assistant integration.
   PRs after adoption sign the CLA on submission. This alone is a
   Phase-0 item and worth doing regardless of the licence outcome.
2. **Relicense the core.** Replace `LICENSE-APACHE` and
   `LICENSE-MIT` with a single `LICENSE` file containing FSL-1.1-Apache-2.0.
   Update every source file's SPDX header:
   `// SPDX-License-Identifier: FSL-1.1-Apache-2.0` (previously
   `Apache-2.0 OR MIT`).
3. **Update `README.md` license badge.** Change from
   `Apache-2.0 OR MIT` to `FSL-1.1-Apache-2.0`.
4. **Publish a CHANGELOG entry** under `## [Unreleased]` →
   `### Licensing` explaining the choice and linking to this
   document.
5. **Announce.** Blog post + HN + Lobsters. Precedent: Sentry's
   ["Introducing the Functional Source License"](https://blog.sentry.io/introducing-the-functional-source-license-freedom-without-free-riding/).
6. **Optionally engage counsel** for a $500–$1,500 review. Fossa
   offers a fixed-price review path.

## Open questions for the maintainer

1. Are you willing to accept the two-year commercial-use
   restriction in exchange for the anti-fork protection?
2. Have any contributors already committed under Apache-2.0 / MIT
   who would need to sign a CLA retroactively? (git log audit
   required; today the answer appears to be "no external
   contributors yet" per §7 of the rating.)
3. Do you have counsel access, or does the maintainer prefer a
   Fossa-style flat-fee review?
4. Do you want to defer this to Phase 1 (after positioning reset)
   or do it in Phase 0 so the relicensing announcement can be
   part of the "we're serious about enterprise" narrative?

## Precedents

- [Sentry — Introducing FSL (2023-11)](https://blog.sentry.io/introducing-the-functional-source-license-freedom-without-free-riding/)
- [HashiCorp — BUSL v1.1 (2023-08)](https://www.hashicorp.com/blog/hashicorp-adopts-business-source-license)
- [Elastic — Elastic Licence + SSPL (2021)](https://www.elastic.co/blog/why-license-change-2021)
- [MongoDB — SSPL (2018)](https://www.mongodb.com/legal/licensing/server-side-public-license)
- [FOSSA — Fall 2024 licensing roundup](https://fossa.com/blog/fall-2024-software-licensing-roundup/)
- [TechCrunch — Fair-source movement (2024-09)](https://techcrunch.com/2024/09/22/some-startups-are-going-fair-source-to-avoid-the-pitfalls-of-open-source-licensing/)
- [Armin Ronacher — FSL vs AGPL for open businesses](https://lucumr.pocoo.org/2024/9/23/fsl-agpl-open-source-businesses/)
- [The BUSL Factor](https://cra.mr/the-busl-factor/)
