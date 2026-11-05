---
paths:
  - "**/*.sql"
  - "internal/store/**"
---

# SQL

- Queries live in `.sql` files, never in Go string literals.
  sqlc generates the Go code that runs them.
- Schema changes are goose migrations.
  Add a new migration rather than editing one that has been released, because existing databases have already run it.
- Every query that returns task data takes the caller's principal and filters on project membership.
  See the [access rules in the design doc](../docs/design.md#access).
