# Public API rate limits

Last updated 2025-02-10.

Limits are per API key and counted per rolling minute.

| API version | Requests per minute | Burst |
|---|---|---|
| v1 (deprecated) | 100 | 20 |
| v2 | 600 | 50 |

Over the limit the API answers 429 with a Retry-After header. Enterprise
customers can ask for a higher limit through their account manager.
