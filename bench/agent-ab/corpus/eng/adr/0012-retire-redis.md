# ADR-0012: Retire the Redis cluster

Status: Accepted.
Date: 2025-01-20

## Context

The Redis cluster now holds only web sessions. It had two outages in 2024 and
costs more than the rest of the web tier combined.

## Decision

Move web sessions to signed, encrypted cookies (stateless). Nothing is stored
server-side. Decommission Redis after one release with both paths.

## Consequences

Session size is limited to 4 KB. Revoking one session needs a deny list, kept
in Postgres.
