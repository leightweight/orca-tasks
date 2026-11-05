---
paths:
  - "**/*.sql"
  - "internal/store/**"
---

# SQL

- Queries live in `.sql` files under `internal/store/queries/`, never in Go string literals.
  sqlc generates the Go code that runs them into `internal/store/db`.
- Schema changes are goose migrations in `internal/store/migrations/`.
  Add a new migration rather than editing one that has been released, because existing databases have already run it.
- Migrations only go forward, with no `-- +goose Down` section.
- Every query that returns task data takes the caller's principal and filters on project membership.
  See the [access rules in the design doc](../docs/design.md#access).
- steveql-fmt formats SQL.
  Write the query, then run `mise run lint-fix` instead of aligning it by hand.
