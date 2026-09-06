# Incident postmortems

Public incident postmortems live in this directory. The
maintainer commits to publishing a postmortem within 7 days of
any customer-affecting incident, using
[`TEMPLATE.md`](./TEMPLATE.md) as the starting structure.

## Why public postmortems

- **Regulated buyers** need to see the vendor's incident-handling
  discipline before they'll deploy. A stack of published
  postmortems is the highest-signal artefact — better than any
  SOC 2 summary or ISO certificate.
- **Community trust** compounds when failures are handled
  transparently.
- **Future-us** benefits from remembering what actually
  happened, not the story we told ourselves later.

## Template

Every postmortem uses [`TEMPLATE.md`](./TEMPLATE.md) — the
structure is required so the reader always knows where to find
timeline vs root cause vs action items. Compliance-framework
notification decisions live at the top of every doc so an
auditor can filter for them quickly.

## Naming convention

Filenames: `NNNN-<slug>.md` where `NNNN` is a monotonic
four-digit number, and `<slug>` is a short kebab-case summary
(e.g. `0001-whatsapp-stream-replaced-outage.md`).

Numbers are assigned in incident-open order, not filed-order.
Gaps allowed for redacted / private incidents.

## Related

- [`TEMPLATE.md`](./TEMPLATE.md) — the postmortem structure
- [`../../SECURITY.md`](../../SECURITY.md) — vulnerability
  reporting + response SLAs
- [`../compliance/gdpr.md`](../compliance/gdpr.md) — Article
  33/34 breach-notification obligations
- [`../compliance/dora.md`](../compliance/dora.md) — Article
  19 major-incident notification
- [`../compliance/hipaa.md`](../compliance/hipaa.md) —
  § 164.400 breach notification
- [`../compliance/eu-ai-act.md`](../compliance/eu-ai-act.md) —
  Article 79 serious-incident notification
