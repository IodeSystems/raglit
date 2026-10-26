# Runbook: rolling back

1. In the deploy tool, open the service and choose the last good release.
2. Roll back `prod-east` and `prod-west` together; do not wait for a bake.
3. Post in #deploys with the release tags and the reason.
4. If the bad release ran a database migration, do not roll back the code
   until the migration owner confirms the old code can read the new schema.
