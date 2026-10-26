# Incident response: the first fifteen minutes

For whoever is paged.

1. Acknowledge the page within 5 minutes, so it does not escalate to the
   secondary.
2. Open an incident channel named `#inc-<yyyymmdd>-<short-name>` and post the
   pager link in it.
3. Set a severity from the table below. If unsure, pick the higher one.
4. You are the incident commander until you hand it over explicitly. Your job
   is coordination, not fixing: ask for help early.
5. Post a status line in the channel every 30 minutes, even if nothing changed.

| Severity | Meaning |
|---|---|
| SEV-1 | customers cannot pay, or data loss |
| SEV-2 | a customer-facing feature is broken |
| SEV-3 | degraded, with a workaround |

After it ends, the commander schedules the review within five business days.
