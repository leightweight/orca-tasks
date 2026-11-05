-- name: InsertComment :one
INSERT INTO comments
    (project_id
    ,issue_id
    ,author_id
    ,body)
VALUES
    (@project_id
    ,@issue_id
    ,@author_id
    ,@body)
RETURNING
    *;

-- name: ListComments :many
SELECT   sqlc.embed(c)
        ,u.username                                                 AS  author_username
        ,u.display_name                                             AS  author_name
        ,u.email                                                    AS  author_email
        ,(m.removed_at IS NULL AND u.disabled_at IS NULL)::BOOLEAN  AS  author_active
FROM    comments    c
JOIN    memberships m   ON  m.id = c.author_id
JOIN    principals  u   ON  u.id = m.principal_id
WHERE   c.issue_id = @issue_id
ORDER BY c.created_at
        ,c.id;
