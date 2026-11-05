-- name: InsertAttachment :one
INSERT INTO attachments
    (project_id
    ,issue_id
    ,uploader_id
    ,filename
    ,mime_type
    ,size
    ,sha256)
VALUES
    (@project_id
    ,@issue_id
    ,@uploader_id
    ,@filename
    ,@mime_type
    ,@size
    ,@sha256)
RETURNING
    *;

-- name: ListAttachments :many
-- The attachments of several issues at once, so a page of issues costs one
-- query rather than one per issue.
SELECT   sqlc.embed(t)
        ,u.username                                                 AS  uploader_username
        ,u.display_name                                             AS  uploader_name
        ,u.email                                                    AS  uploader_email
        ,(m.removed_at IS NULL AND u.disabled_at IS NULL)::BOOLEAN  AS  uploader_active
FROM    attachments t
JOIN    memberships m   ON  m.id = t.uploader_id
JOIN    principals  u   ON  u.id = m.principal_id
WHERE   t.issue_id = ANY(@issue_ids::BIGINT[])
ORDER BY t.issue_id
        ,t.created_at
        ,t.id;

-- name: VisibleAttachment :one
-- An attachment, if the principal can see its issue, with the principal's
-- membership in the project. An agent acts for its owner, so the account of
-- the uploader and of the caller, and the account's role, decide who can
-- delete it.
SELECT   sqlc.embed(t)
        ,sqlc.embed(m)
        ,i.number
        ,COALESCE(upp.owner_id, upp.id)::BIGINT AS  uploader_account
        ,COALESCE(cp.owner_id, cp.id)::BIGINT   AS  caller_account
        ,COALESCE(am.role, m.role)::TEXT        AS  account_role
FROM        attachments t
JOIN        issues      i   ON  i.id = t.issue_id
JOIN        memberships m   ON  m.project_id = t.project_id
JOIN        principals  cp  ON  cp.id = m.principal_id
JOIN        memberships up  ON  up.id = t.uploader_id
JOIN        principals  upp ON  upp.id = up.principal_id
LEFT JOIN   memberships am  ON  am.project_id = t.project_id
                            AND am.principal_id = COALESCE(cp.owner_id, cp.id)
                            AND am.removed_at IS NULL
WHERE   t.id = @id
        AND m.principal_id = @principal_id
        AND m.removed_at IS NULL
        AND (
            sqlc.narg(project_ids)::BIGINT[] IS NULL
            OR  m.project_id = ANY(sqlc.narg(project_ids)::BIGINT[])
        );

-- name: DeleteAttachment :exec
DELETE
FROM    attachments
WHERE   id = @id;

-- name: ExistingAttachments :many
-- Which of these IDs still have rows, for the cleanup pass over the files.
SELECT  id
FROM    attachments
WHERE   id = ANY(@ids::BIGINT[]);
