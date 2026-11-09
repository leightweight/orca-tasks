-- name: VisibleIssues :many
-- The issues a principal can see, under its keys for their projects, with
-- their people and parents resolved. A parent is always in the issue's
-- project, so the caller can see it too. A key limited to some projects passes their IDs; a
-- null array means every project. The other filters narrow it to one issue.
SELECT   sqlc.embed(i)
        ,m.key                                                      AS  project_key
        ,p.name                                                     AS  project_name
        ,r.username                                                 AS  reporter_username
        ,r.display_name                                             AS  reporter_name
        ,r.email                                                    AS  reporter_email
        ,(rm.removed_at IS NULL AND r.disabled_at IS NULL)::BOOLEAN AS  reporter_active
        ,a.username                                                 AS  assignee_username
        ,a.display_name                                             AS  assignee_name
        ,a.email                                                    AS  assignee_email
        ,(am.removed_at IS NULL AND a.disabled_at IS NULL)::BOOLEAN AS  assignee_active
        ,pi.number                                                  AS  parent_number
        ,pi.summary                                                 AS  parent_summary
        ,pi.status_id                                               AS  parent_status_id
        ,pi.type_id                                                 AS  parent_type_id
FROM        memberships m
JOIN        projects    p   ON  p.id = m.project_id
JOIN        issues      i   ON  i.project_id = m.project_id
JOIN        memberships rm  ON  rm.id = i.reporter_id
JOIN        principals  r   ON  r.id = rm.principal_id
LEFT JOIN   memberships am  ON  am.id = i.assignee_id
LEFT JOIN   principals  a   ON  a.id = am.principal_id
LEFT JOIN   issues      pi  ON  pi.id = i.parent_id
WHERE   m.principal_id = @principal_id
        AND m.removed_at IS NULL
        AND (
            sqlc.narg(project_ids)::BIGINT[] IS NULL
            OR  m.project_id = ANY(sqlc.narg(project_ids)::BIGINT[])
        )
        AND (
            sqlc.narg(key)::TEXT IS NULL
            OR  m.key = UPPER(sqlc.narg(key)::TEXT)
        )
        AND (
            sqlc.narg(number)::INTEGER IS NULL
            OR  i.number = sqlc.narg(number)::INTEGER
        )
        AND (
            sqlc.narg(issue_id)::BIGINT IS NULL
            OR  i.id = sqlc.narg(issue_id)::BIGINT
        )
        AND (
            sqlc.narg(parent_id)::BIGINT IS NULL
            OR  i.parent_id = sqlc.narg(parent_id)::BIGINT
        );

-- name: NextIssueNumber :one
UPDATE  projects
SET     next_number = next_number + 1
WHERE   id = @id
RETURNING
    (next_number - 1)::INTEGER  AS  number;

-- name: InsertIssue :one
INSERT INTO issues
    (project_id
    ,number
    ,summary
    ,description
    ,status_id
    ,type_id
    ,priority_id
    ,reporter_id
    ,assignee_id
    ,parent_id
    ,labels)
VALUES
    (@project_id
    ,@number
    ,@summary
    ,@description
    ,@status_id
    ,@type_id
    ,@priority_id
    ,@reporter_id
    ,@assignee_id
    ,@parent_id
    ,@labels)
RETURNING
    *;

-- name: UpdateIssue :exec
UPDATE  issues
SET      summary        = @summary
        ,description    = @description
        ,status_id      = @status_id
        ,type_id        = @type_id
        ,priority_id    = @priority_id
        ,assignee_id    = @assignee_id
        ,parent_id      = @parent_id
        ,labels         = @labels
        ,updated_at     = NOW()
WHERE   id = @id;

-- name: TouchIssue :exec
UPDATE  issues
SET     updated_at = NOW()
WHERE   id = @id;

-- name: InsertHistory :copyfrom
INSERT INTO issue_history
    (project_id
    ,issue_id
    ,actor_id
    ,field
    ,old_value
    ,new_value)
VALUES
    (@project_id
    ,@issue_id
    ,@actor_id
    ,@field
    ,@old_value
    ,@new_value);

-- name: ListHistory :many
SELECT  *
FROM    issue_history
WHERE   issue_id = @issue_id
ORDER BY created_at
        ,id;

-- name: ActiveMembership :one
-- The membership a user has in a project, if it's active and the user isn't
-- disabled. Assignment looks people up this way; agents can't be assignees.
SELECT  m.id
FROM    memberships m
JOIN    principals  u   ON  u.id = m.principal_id
WHERE   m.project_id = @project_id
        AND u.username = LOWER(@username)
        AND m.removed_at IS NULL
        AND u.disabled_at IS NULL
        AND u.kind = 'user';

-- name: SearchPeople :many
-- The users that share a project with the caller, optionally in one project,
-- and matching a search on username or display name. Agents aren't listed:
-- nobody picks them.
SELECT DISTINCT
         u.username
        ,u.display_name
        ,u.email
FROM    memberships mine
JOIN    memberships other   ON  other.project_id = mine.project_id
                            AND other.removed_at IS NULL
JOIN    principals  u       ON  u.id = other.principal_id
                            AND u.disabled_at IS NULL
                            AND u.kind = 'user'
WHERE   mine.principal_id = @principal_id
        AND mine.removed_at IS NULL
        AND (
            sqlc.narg(project_ids)::BIGINT[] IS NULL
            OR  mine.project_id = ANY(sqlc.narg(project_ids)::BIGINT[])
        )
        AND (
            sqlc.narg(project_id)::BIGINT IS NULL
            OR  mine.project_id = sqlc.narg(project_id)::BIGINT
        )
        AND (
            @search::TEXT = ''
            OR  u.username ILIKE '%' || @search::TEXT || '%'
            OR  u.display_name ILIKE '%' || @search::TEXT || '%'
        )
ORDER BY u.username
LIMIT   @max_results;

-- name: LockIssue :one
-- An issue, its assignee's username, and its parent's number, locked until the transaction ends, so
-- concurrent edits apply one after the other instead of overwriting each
-- other.
SELECT   sqlc.embed(i)
        ,a.username AS  assignee_username
        ,pi.number  AS  parent_number
FROM        issues      i
LEFT JOIN   memberships am  ON  am.id = i.assignee_id
LEFT JOIN   principals  a   ON  a.id = am.principal_id
LEFT JOIN   issues      pi  ON  pi.id = i.parent_id
WHERE   i.id = @id
FOR UPDATE OF i;

-- name: LockIssueProject :exec
-- Locks an issue's project, so changes to parents in it apply one at a time
-- and two of them can't make a cycle together. It takes the lock that
-- numbering a new issue does, so callers take it before locking any issue.
SELECT  p.id
FROM    projects p
WHERE   p.id = (
            SELECT  i.project_id
            FROM    issues i
            WHERE   i.id = @issue_id
        )
FOR NO KEY UPDATE;

-- name: IssueAncestors :many
-- An issue's ID and the IDs of its parent, its parent's parent, and so on.
WITH RECURSIVE ancestors AS (
        SELECT   i.id
                ,i.parent_id
        FROM    issues  i
        WHERE   i.id = @id

        UNION

        SELECT   i.id
                ,i.parent_id
        FROM    issues      i
        JOIN    ancestors   a   ON  a.parent_id = i.id
    )
SELECT  id
FROM    ancestors;
