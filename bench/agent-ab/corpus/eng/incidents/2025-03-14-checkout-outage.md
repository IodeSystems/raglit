# Incident report: checkout failures, 2025-03-14

Severity: SEV-1. Duration: 2 h 47 min (09:12 to 11:59 UTC).

## Summary

Customers could not complete checkout. The web app showed "payment could not
be processed" for every order.

## Timeline

- 09:12 error rate on checkout rises to 100%
- 09:20 billing on-call paged; billing-svc logs show handshake failures
- 10:05 traced to calls from billing-svc to ledger-svc
- 11:40 new internal certificate installed on ledger-svc
- 11:59 checkout error rate back to baseline

## Root cause

The internal TLS certificate on ledger-svc expired at 09:11 UTC. Renewal was a
manual calendar task and the reminder went to a former employee's mailbox.
billing-svc refused the expired certificate (correctly) and, because it never
charges without a ledger entry, every checkout failed.

## Action items

1. Automate internal certificate renewal. Owner: TBD (to be decided in the ops
   thread).
2. Alert 14 days before any internal certificate expires. Owner: infra.
3. Move calendar reminders to a team alias. Owner: People Operations.
