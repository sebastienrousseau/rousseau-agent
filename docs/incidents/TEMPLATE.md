# INCIDENT NNNN — <one-line summary>

**Status:** DRAFT / INVESTIGATING / MITIGATED / RESOLVED / POSTMORTEM_PUBLISHED
**Severity:** SEV-1 / SEV-2 / SEV-3
**Reported:** YYYY-MM-DD HH:MM UTC by <reporter>
**Resolved:** YYYY-MM-DD HH:MM UTC (duration: NNhNNm)
**Affected components:** <list — e.g. WhatsApp bridge, claudecli provider, session store>
**Compliance framework triggers:**
- GDPR Article 33/34 breach notification: YES / NO (justify)
- DORA Article 19 major-incident notification: YES / NO (justify)
- HIPAA § 164.400 breach: YES / NO (justify)
- EU AI Act Article 79 serious-incident notification: YES / NO (justify)

Delete rows that don't apply; **keep the NO rows with
justification** — the auditor's question is "did you consider
it and decide no", not "did you never look at it."

---

## Summary

Three sentences. What broke, who noticed, how long it lasted.
This is what appears in your status page + your public
postmortem — write it first so the rest of the doc has an
anchor.

## Impact

Quantified:

- **Users affected:** <count or "unknown, upper bound N">
- **Sessions affected:** <SQL query below>
- **Messages delayed / dropped:** <estimate + method>
- **Data exposure:** <NONE / description>
- **Financial impact:** <if applicable>

SQL for the impact denominator (adapt for postgres):

```sql
-- Sessions active during the incident window
SELECT count(*) FROM sessions
WHERE updated_at BETWEEN '<incident_start>' AND '<incident_end>';

-- Turn success rate during the window
SELECT dimension, sub_metric, avg(value)
FROM reliability_samples
WHERE at BETWEEN '<incident_start>' AND '<incident_end>'
  AND dimension = 'safety' AND sub_metric = 'turn'
GROUP BY 1, 2;

-- Violations recorded during the window (Enterprise Edition
-- audit egress is the authoritative source; this is the
-- in-daemon shadow copy).
SELECT metadata->>'constraint' AS rule,
       metadata->>'severity'   AS severity,
       count(*)                AS n
FROM reliability_samples
WHERE at BETWEEN '<incident_start>' AND '<incident_end>'
  AND dimension = 'safety' AND sub_metric = 'violation'
GROUP BY 1, 2 ORDER BY 3 DESC;
```

## Timeline

Times are UTC. Use `journalctl --user -u rousseau-agent
--since '...' --until '...'` for the base timeline.

| Time | Event | Source |
|---|---|---|
| HH:MM | <initial trigger — commit / deploy / upstream outage / user report> | <logs / issue / user report> |
| HH:MM | First user report | <channel> |
| HH:MM | On-call paged | <PagerDuty / Slack> |
| HH:MM | Detected via `<alert name>` | <Grafana / Alertmanager> |
| HH:MM | Root cause hypothesised: <hypothesis> | <who + reasoning> |
| HH:MM | Mitigation attempt #1: <action> — <outcome> | — |
| HH:MM | Mitigation attempt #2: <action> — <outcome> | — |
| HH:MM | Service restored | <verification step> |
| HH:MM | Public status update | <status page URL> |

## Root cause

Single paragraph. What actually broke. Cite the specific commit,
config change, upstream failure, or environmental condition. If
"we don't know" — write that, name what would help you know, and
add an action item to add that instrumentation.

## What went well

Bullet list. Detection speed, communication cadence, mitigation
that worked, tooling that saved time. Positive framing is
load-bearing for the next incident's response — teams that only
document what went wrong quietly lose institutional knowledge of
what to keep doing.

## What went badly

Bullet list. Detection lag, communication gaps, dead-ends in
mitigation, missing runbooks, silent failure modes, alert
storms, etc. **Blameless framing** — describe the failure
mode, not the person.

## Lessons

Bullet list of the takeaways worth remembering after the action
items ship. This is what a future engineer reading a year later
takes away.

## Action items

Every action item has an owner + a rough date. Categorise
using standard postmortem taxonomy so trends surface across
incidents:

| # | Action | Owner | Category | Target date | Ticket |
|---|---|---|---|---|---|
| 1 | <action> | <name> | Detection / Prevention / Mitigation / Documentation / Process | YYYY-MM-DD | <link> |

Categories at-a-glance:

- **Detection**: alerts, dashboards, monitoring — "how would we
  see this faster next time?"
- **Prevention**: code changes, config changes, tests — "how
  would this not happen again?"
- **Mitigation**: runbooks, tooling, automation — "if it does
  happen again, how do we recover faster?"
- **Documentation**: docs, comments, examples — "how do we
  transfer this knowledge?"
- **Process**: on-call rotation, escalation policy, comms
  templates — "how do we operate better?"

## References

- Related incidents: <#NNNN — one-line>
- Upstream advisory / vendor CVE / provider incident report:
  <URL>
- Postmortem publication: <blog URL / public link>
- SIEM query for full forensics: <query>

---

## Signoff

- [ ] Timeline complete + verified against logs
- [ ] Impact quantified with SQL queries above
- [ ] Root cause specific + citable
- [ ] Every action item has an owner + date
- [ ] Blameless language throughout
- [ ] Compliance-notification decisions justified (top of doc)
- [ ] Signed off by: <name>, <name>
