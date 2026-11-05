-- name: CreateAPIKey :one
INSERT INTO api_keys
    (principal_id
    ,name
    ,key_hash
    ,read_only
    ,all_projects
    ,expires_at)
VALUES
    (@principal_id
    ,@name
    ,@key_hash
    ,@read_only
    ,@all_projects
    ,@expires_at)
RETURNING
    id;

-- name: AddAPIKeyProjects :exec
INSERT INTO api_key_projects
    (api_key_id
    ,project_id)
SELECT   @api_key_id
        ,unnest(@project_ids::BIGINT[]);

-- name: ListAPIKeys :many
-- A user's keys and their agents' keys, with the projects a limited key can
-- reach, under the user's keys for them.
SELECT   sqlc.embed(k)
        ,p.username
        ,COALESCE((SELECT ARRAY_AGG(m.key ORDER BY m.key) FROM api_key_projects kp JOIN memberships m ON m.project_id = kp.project_id AND m.principal_id = @owner_id WHERE kp.api_key_id = k.id), '{}')::TEXT[] AS  project_keys
FROM    api_keys    k
JOIN    principals  p   ON  p.id = k.principal_id
WHERE   p.id = @owner_id
        OR  p.owner_id = @owner_id
ORDER BY k.revoked_at IS NOT NULL
        ,k.created_at DESC;

-- name: RevokeAPIKey :execrows
UPDATE  api_keys    k
SET     revoked_at = NOW()
FROM    principals  p
WHERE   k.id = @id
        AND p.id = k.principal_id
        AND (
            p.id = @owner_id
            OR  p.owner_id = @owner_id
        )
        AND k.revoked_at IS NULL;

-- name: AuthenticateAPIKey :one
-- A key works while it's live and neither its principal nor, for an agent,
-- the owner is disabled or still enrolling. The user is the principal, or
-- the agent's owner.
SELECT   k.id
        ,k.read_only
        ,k.all_projects
        ,p.id                                                                                                               AS  principal_id
        ,p.username
        ,p.display_name
        ,p.email
        ,u.username                                                                                                         AS  user_username
        ,COALESCE((SELECT ARRAY_AGG(kp.project_id) FROM api_key_projects kp WHERE kp.api_key_id = k.id), '{}')::BIGINT[]    AS  project_ids
FROM    api_keys    k
JOIN    principals  p   ON  p.id = k.principal_id
JOIN    principals  u   ON  u.id = COALESCE(p.owner_id, p.id)
WHERE   k.key_hash = @key_hash
        AND k.revoked_at IS NULL
        AND (
            k.expires_at IS NULL
            OR  k.expires_at > NOW()
        )
        AND p.disabled_at IS NULL
        AND u.disabled_at IS NULL
        AND u.totp_secret IS NOT NULL;

-- name: TouchAPIKey :exec
UPDATE  api_keys
SET     last_used_at = NOW()
WHERE   id = @id
        AND (
            last_used_at IS NULL
            OR  last_used_at < NOW() - INTERVAL '1 minute'
        );
