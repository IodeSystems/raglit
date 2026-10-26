# ADR-0009: Postgres for the ledger

Status: Accepted.
Date: 2023-09-11

## Decision

ledger-svc stores entries in its own Postgres cluster, `pg-ledger`, with
logical replication to the analytics warehouse.
