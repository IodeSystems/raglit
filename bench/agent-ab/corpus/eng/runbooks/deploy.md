# Runbook: deploying a service

1. Merge to `main`; CI builds and pushes the image.
2. Open a deploy request in the deploy tool. Name the service and the image
   tag.
3. The deploy tool rolls out to `prod-east` first, waits 20 minutes watching
   error rate and latency, then rolls out to `prod-west`.
4. Do not deploy on Fridays after 14:00 local time or during a freeze.

If error rate rises above 2% during the bake, the tool stops the rollout. See
the rollback runbook.
