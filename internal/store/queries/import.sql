-- name: ImportIssues :copyfrom
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
    ,labels
    ,created_at
    ,updated_at)
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
    ,@labels
    ,@created_at
    ,@updated_at);

-- name: IssueNumbers :many
SELECT   id
        ,number
FROM    issues
WHERE   project_id = @project_id;

-- name: ImportComments :copyfrom
INSERT INTO comments
    (project_id
    ,issue_id
    ,author_id
    ,body
    ,created_at
    ,updated_at)
VALUES
    (@project_id
    ,@issue_id
    ,@author_id
    ,@body
    ,@created_at
    ,@updated_at);

-- name: RaiseNextNumber :exec
-- Imported issues keep their numbers, so the counter moves past them, and
-- past any number the old tracker had already used up.
UPDATE  projects
SET     next_number = GREATEST(next_number, @next_number)
WHERE   id = @id;
