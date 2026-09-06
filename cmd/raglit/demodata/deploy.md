# Deployment

We ship with blue-green deploys. The new version boots on the green fleet while
blue keeps serving; once green passes health checks, the load balancer cuts over
to it in one atomic switch.

Rollback is the same switch in reverse: flip the load balancer back to blue. No
redeploy is needed, so recovery is seconds, not minutes.

Database migrations must be backward compatible for one release, so blue and
green can run against the same schema during a cutover.
