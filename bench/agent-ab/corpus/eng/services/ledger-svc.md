# ledger-svc

Owner team: ledger (manager: Wren Halvorsen).

Double-entry ledger of every financial event. Append-only.

## Runtime

- Port 9443 (HTTPS, internal only; mutual TLS)
- Internal certificate issued by the platform CA, 90-day lifetime
- Postgres cluster `pg-ledger`

## On-call

Pager rotation `ledger-primary`. Escalation after 15 minutes to
`platform-secondary`. Slack channel #ledger-oncall.
