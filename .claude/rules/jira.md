---
paths:
  - "internal/jira/**"
  - "internal/jql/**"
---

# Jira-compatible API

- Implement the Jira Server and Data Center REST API v2, not Jira Cloud's v3.
  Version 2 sends descriptions and comments as plain text, so Markdown reaches Orca unchanged.
- Return only the fields that Orca or the CLI reads.
  When you add a field, note which caller reads it.
- Orca's Jira integration is the compatibility target.
  When a response shape is in doubt, check what Orca's Jira mappers read before guessing from Atlassian's documentation.
- Errors use Jira's `errorMessages` and `errors` envelope, because Orca builds its error text from them.
