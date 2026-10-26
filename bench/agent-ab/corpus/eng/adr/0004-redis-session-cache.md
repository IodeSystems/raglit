# ADR-0004: Store web sessions in Redis

Status: Superseded by ADR-0012.
Date: 2022-06-02

## Context

Web sessions are in process memory, so a deploy logs every user out.

## Decision

Store sessions in a shared Redis cluster with a 24 hour TTL.

## Consequences

One more stateful service to run. Session reads add a network hop.
