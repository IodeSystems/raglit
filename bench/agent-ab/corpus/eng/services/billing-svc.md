# billing-svc

Owner team: billing (manager: Calder Byrne).

Computes invoices from metered sensor usage and charges customers through the
payment provider.

## Runtime

- Port 8443 (HTTPS), health check at /healthz
- Deployed to the `prod-east` and `prod-west` clusters
- Config in `deploy/billing/values.yaml`

## Dependencies

- ledger-svc: every invoice line is posted to the ledger before the charge is
  attempted. If ledger-svc is down, billing-svc queues charges and retries; it
  does not charge without a ledger entry.
- the payment provider's API
- Postgres cluster `pg-billing`

## On-call

The billing team carries its own pager, rotation `billing-primary`, Slack
channel #billing-oncall.
