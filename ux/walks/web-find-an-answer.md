# Walk: web-find-an-answer

Job: find the answer to a question, and the document that says it.
Scenario: web-find-an-answer
Mode: read-only

| step | start | promise |
|---|---|---|
| `01-what-is-this` | `/` | I can tell what this is for, and that my company's documents are in it |
| `02-ask` | | I can find where to search, and search in my own words |
| `03-the-answer` | `/p/larkspur/search?q=why%20did%20checkout%20break%20in%20March` | I can find the answer among the results without opening every one |
| `04-the-source` | | I can open the document the answer came from and see the passage in it |

Step 03 starts on a results page: the read-only walk refuses Enter after
typing, and Enter is how this search box searches. Remove the start when the
walk lets a search through.
