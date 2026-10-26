# Alerting policy

## Who gets woken up

An alert pages a person outside working hours only if customers are affected
now: checkout, sign-in, sensor data ingestion, or the public API failing or
badly degraded. Everything else is a ticket for the owning team's next working
day.

## Every paging alert must have

- a runbook link,
- an owner team,
- a threshold that has been reviewed in the last six months.

An alert that paged and needed no action is either fixed or downgraded to a
ticket within a week.
