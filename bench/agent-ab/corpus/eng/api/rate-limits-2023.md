# Public API rate limits

Last updated 2023-05-02.

Limits are per API key and counted per rolling minute.

| API version | Requests per minute | Burst |
|---|---|---|
| v1 | 100 | 20 |
| v2 (beta) | 300 | 30 |

Over the limit the API answers 429 with a Retry-After header.
