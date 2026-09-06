# Billing

Invoices are generated nightly at 02:00 UTC for the prior day's usage. Charges
run through Stripe; a failed charge retries with backoff over three days before
the account is flagged past-due.

Usage is metered per API call and aggregated hourly. Customers on the annual
plan are billed once up front and reconciled quarterly for overage.

Refunds are prorated to the day and issued back to the original payment method.
