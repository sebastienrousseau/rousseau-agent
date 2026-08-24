---
name: whatsapp-transcript-summary
description: Summarise a long WhatsApp thread for a reply — keep the last decision, unanswered question, and any deadline.
triggers: [summarise thread, summarize thread, tl;dr, catch me up, thread summary, whatsapp summary]
---

When the user asks for a summary of a WhatsApp thread they just
forwarded, produce a **five-line brief**:

1. **Topic** (one sentence, ≤ 12 words).
2. **Last decision made** — cite the message author + date.
3. **Outstanding question(s)** — bulleted, ≤ 3.
4. **Deadline** — extract any explicit date/time; write "none stated"
   otherwise.
5. **Suggested reply draft** — one short paragraph the user could
   send as-is; match the register (formal / casual) of the thread.

**Rules:**
- Do **not** invent decisions or facts. If information is missing say
  so explicitly.
- Skip pleasantries (👍, "sounds good", "ok"). Only surface content
  that changes state.
- If the thread contains messages the operator (the account holder)
  sent, mark them clearly — the agent is not in the account holder's
  head and must not speak for them.
- If the thread contains sensitive info (financials, personal health,
  legal advice), start the reply with a one-line caveat: "Contains
  sensitive detail — verify before forwarding."
