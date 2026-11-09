-- name: InsertProject :one
INSERT INTO projects
    (key
    ,name)
VALUES
    (@key
    ,@name)
RETURNING
    *;

-- name: GetProject :one
SELECT  *
FROM    projects
WHERE   id = @id;

-- name: RenameProject :exec
UPDATE  projects
SET     name = @name
WHERE   id = @id;

-- name: DeleteProject :exec
DELETE
FROM    projects
WHERE   id = @id;

-- name: ActiveKeys :many
SELECT  key
FROM    memberships
WHERE   principal_id = @principal_id
        AND removed_at IS NULL;

-- name: UpsertMembership :one
-- Adding someone who was removed restores their old membership, so the
-- issues and comments that reference it are theirs again.
INSERT INTO memberships
    (project_id
    ,principal_id
    ,role
    ,key)
VALUES
    (@project_id
    ,@principal_id
    ,@role
    ,@key)
ON CONFLICT (project_id, principal_id) DO
    UPDATE SET
         role       = excluded.role
        ,key        = excluded.key
        ,removed_at = NULL
RETURNING
    *;

-- name: SyncAgentMemberships :exec
-- Gives a user's agents the same memberships as the user, under the same keys.
-- A null project syncs every project, for a new agent.
INSERT INTO memberships
    (project_id
    ,principal_id
    ,role
    ,key
    ,removed_at)
SELECT   m.project_id
        ,a.id
        ,'member'
        ,m.key
        ,m.removed_at
FROM    memberships m
JOIN    principals  a   ON  a.owner_id = m.principal_id
WHERE   m.principal_id = @user_id
        AND (
            sqlc.narg(project_id)::BIGINT IS NULL
            OR  m.project_id = sqlc.narg(project_id)
        )
ON CONFLICT (project_id, principal_id) DO
    UPDATE SET
         key        = excluded.key
        ,removed_at = excluded.removed_at;

-- name: RemoveMembership :execrows
UPDATE  memberships
SET     removed_at = NOW()
WHERE   project_id = @project_id
        AND principal_id = @user_id
        AND removed_at IS NULL;

-- name: SetMembershipKey :execrows
UPDATE  memberships
SET     key = @key
WHERE   project_id = @project_id
        AND principal_id = @user_id
        AND removed_at IS NULL;

-- name: SetMembershipRole :execrows
UPDATE  memberships
SET     role = @role
WHERE   project_id = @project_id
        AND principal_id = @user_id
        AND removed_at IS NULL;

-- name: GetMembershipByKey :one
-- A principal's project under the key it sees the project by.
SELECT   sqlc.embed(m)
        ,sqlc.embed(p)
FROM    memberships m
JOIN    projects    p   ON  p.id = m.project_id
WHERE   m.principal_id = @principal_id
        AND m.key = UPPER(@key)
        AND m.removed_at IS NULL;

-- name: GetMembership :one
SELECT  *
FROM    memberships
WHERE   project_id = @project_id
        AND principal_id = @principal_id
        AND removed_at IS NULL;

-- name: ListMyProjects :many
SELECT   sqlc.embed(m)
        ,sqlc.embed(p)
        ,(SELECT COUNT(*) FROM issues i WHERE i.project_id = p.id)::BIGINT                                                                                                  AS  issues
        ,(SELECT COUNT(*) FROM memberships x JOIN principals xp ON xp.id = x.principal_id WHERE x.project_id = p.id AND x.removed_at IS NULL AND xp.kind = 'user')::BIGINT  AS  members
FROM    memberships m
JOIN    projects    p   ON  p.id = m.project_id
WHERE   m.principal_id = @principal_id
        AND m.removed_at IS NULL
ORDER BY m.key;

-- name: ListMembers :many
-- A project's users. Their agents are members too, but aren't listed.
SELECT   sqlc.embed(m)
        ,sqlc.embed(u)
FROM    memberships m
JOIN    principals  u   ON  u.id = m.principal_id
WHERE   m.project_id = @project_id
        AND m.removed_at IS NULL
        AND u.kind = 'user'
ORDER BY u.username;

-- name: ListAllProjects :many
SELECT   sqlc.embed(p)
        ,(SELECT COUNT(*) FROM issues i WHERE i.project_id = p.id)::BIGINT                                                                                                                                                  AS  issues
        ,COALESCE((SELECT ARRAY_AGG(u.username ORDER BY u.username) FROM memberships x JOIN principals u ON u.id = x.principal_id WHERE x.project_id = p.id AND x.removed_at IS NULL AND u.kind = 'user'), '{}')::TEXT[]    AS  members
FROM    projects    p
ORDER BY p.key
        ,p.id;

-- name: LockProject :exec
-- Locks a project's row, so changes to who administers it apply one at a time.
SELECT  id
FROM    projects
WHERE   id = @id
FOR NO KEY UPDATE;

-- name: OtherAdmins :one
-- How many active administrators a project has besides one user. Disabled users
-- count as gone, since they can't sign in to administer it.
SELECT  COUNT(*)
FROM    memberships m
JOIN    principals  p   ON  p.id = m.principal_id
WHERE   m.project_id = @project_id
        AND m.principal_id <> @user_id  -- 🤮
        AND m.role = 'admin'
        AND m.removed_at IS NULL
        AND p.disabled_at IS NULL
        AND p.kind = 'user';
