-- name: ListStatuses :many
SELECT  *
FROM    statuses
ORDER BY position;

-- name: ListIssueTypes :many
SELECT  *
FROM    issue_types
ORDER BY position;

-- name: ListPriorities :many
SELECT  *
FROM    priorities
ORDER BY position;
