# Walk: web-whats-in-it

Job: find out what an index holds and whether it is healthy.
Scenario: web-whats-in-it
Mode: read-only

| step | start | promise |
|---|---|---|
| `01-which-index` | `/` | I can find the index for my folder and tell it apart from any other |
| `02-what-it-holds` | | I can see which documents it holds, and count them |
| `03-all-read` | | I can tell whether every document was read, and which were not |
| `04-anything-wrong` | | I can see whether anything is wrong, and what I would do about it |
