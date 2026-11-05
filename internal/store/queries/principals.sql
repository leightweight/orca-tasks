-- name: HasUsers :one
SELECT  EXISTS (
            SELECT  1
            FROM    principals
            WHERE   kind = 'user'
        );

-- name: CreateUser :one
INSERT INTO principals
    (kind
    ,username
    ,display_name
    ,email
    ,site_admin
    ,password_hash
    ,totp_secret)
VALUES
    ('user'
    ,@username
    ,@display_name
    ,@email
    ,@site_admin
    ,@password_hash
    ,@totp_secret)
RETURNING
    *;

-- name: GetPrincipal :one
SELECT  *
FROM    principals
WHERE   id = @id;

-- name: GetPrincipalByUsername :one
SELECT  *
FROM    principals
WHERE   username = LOWER(@username);

-- name: SetCredentials :exec
UPDATE  principals
SET      password_hash      = @password_hash
        ,totp_secret        = @totp_secret
        ,totp_last_step     = 0
        ,session_version    = session_version + 1
WHERE   id = @id;

-- name: SetPassword :exec
UPDATE  principals
SET      password_hash      = @password_hash
        ,session_version    = session_version + 1
WHERE   id = @id;

-- name: UseTOTPStep :execrows
UPDATE  principals
SET     totp_last_step = @step
WHERE   id = @id
        AND totp_last_step < @step;

-- name: RecordSignIn :exec
UPDATE  principals
SET     last_sign_in_at = NOW()
WHERE   id = @id;

-- name: UpdateProfile :exec
UPDATE  principals
SET      display_name   = @display_name
        ,email          = @email
WHERE   id = @id;

-- name: SetDisabled :exec
UPDATE  principals
SET      disabled_at        = CASE
            WHEN @disabled::BOOLEAN THEN COALESCE(disabled_at, NOW())
         END
        ,session_version    = session_version + 1
WHERE   id = @id;

-- name: SetSiteAdmin :exec
UPDATE  principals
SET     site_admin = @site_admin
WHERE   id = @id
        AND kind = 'user';

-- name: CreateAgent :one
INSERT INTO principals
    (kind
    ,username
    ,display_name
    ,owner_id)
VALUES
    ('agent'
    ,@username
    ,@display_name
    ,@owner_id)
RETURNING
    *;

-- name: ListAgents :many
SELECT  *
FROM    principals
WHERE   owner_id = @owner_id
ORDER BY username;

-- name: UpdateAgent :execrows
UPDATE  principals
SET     display_name = @display_name
WHERE   id = @id
        AND owner_id = @owner_id;

-- name: SetAgentDisabled :execrows
UPDATE  principals
SET     disabled_at = CASE
            WHEN @disabled::BOOLEAN THEN COALESCE(disabled_at, NOW())
        END
WHERE   id = @id
        AND owner_id = @owner_id;

-- name: ListUsers :many
-- One row per user with the counts the administration page shows. An agent's
-- work counts toward its owner, the account. Each table is read once and
-- grouped by account, so the cost doesn't grow with the number of users.
WITH accounts AS (
        SELECT   m.id                       AS  membership_id
                ,COALESCE(p.owner_id, p.id) AS  account_id
        FROM    memberships m
        JOIN    principals  p   ON  p.id = m.principal_id
    )
    ,projects AS (
        SELECT   principal_id   AS  account_id
                ,COUNT(*)       AS  n
        FROM    memberships
        WHERE   removed_at IS NULL
        GROUP BY principal_id
    )
    ,agents AS (
        SELECT   owner_id   AS  account_id
                ,COUNT(*)   AS  n
        FROM    principals
        WHERE   owner_id IS NOT NULL
        GROUP BY owner_id
    )
    ,keys AS (
        SELECT   COALESCE(p.owner_id, p.id)                                                                         AS  account_id
                ,COUNT(*) FILTER (WHERE k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at > NOW()))    AS  active
                ,MAX(k.last_used_at)                                                                                AS  last_used_at
        FROM    api_keys    k
        JOIN    principals  p   ON  p.id = k.principal_id
        GROUP BY COALESCE(p.owner_id, p.id)
    )
    ,reported AS (
        SELECT   a.account_id
                ,COUNT(*)                           AS  n
                ,SUM(octet_length(i.description))   AS  bytes
        FROM    issues      i
        JOIN    accounts    a   ON  a.membership_id = i.reporter_id
        GROUP BY a.account_id
    )
    ,assigned AS (
        SELECT   a.account_id
                ,COUNT(*)   AS  n
        FROM    issues      i
        JOIN    accounts    a   ON  a.membership_id = i.assignee_id
        GROUP BY a.account_id
    )
    ,commented AS (
        SELECT   a.account_id
                ,COUNT(*)                   AS  n
                ,SUM(octet_length(c.body))  AS  bytes
        FROM    comments    c
        JOIN    accounts    a   ON  a.membership_id = c.author_id
        GROUP BY a.account_id
    )
    ,attached AS (
        SELECT   a.account_id
                ,COUNT(*)       AS  n
                ,SUM(t.size)    AS  bytes
        FROM    attachments t
        JOIN    accounts    a   ON  a.membership_id = t.uploader_id
        GROUP BY a.account_id
    )
SELECT   sqlc.embed(u)
        ,COALESCE(projects.n, 0)::BIGINT                                                                    AS  projects
        ,COALESCE(agents.n, 0)::BIGINT                                                                      AS  agents
        ,COALESCE(keys.active, 0)::BIGINT                                                                   AS  active_keys
        ,keys.last_used_at::TIMESTAMPTZ                                                                     AS  last_key_use
        ,COALESCE(reported.n, 0)::BIGINT                                                                    AS  issues_reported
        ,COALESCE(assigned.n, 0)::BIGINT                                                                    AS  issues_assigned
        ,COALESCE(commented.n, 0)::BIGINT                                                                   AS  comments
        ,COALESCE(attached.n, 0)::BIGINT                                                                    AS  attachments
        ,(COALESCE(reported.bytes, 0) + COALESCE(commented.bytes, 0) + COALESCE(attached.bytes, 0))::BIGINT AS  bytes
FROM        principals  u
LEFT JOIN   projects        ON  projects.account_id = u.id
LEFT JOIN   agents          ON  agents.account_id = u.id
LEFT JOIN   keys            ON  keys.account_id = u.id
LEFT JOIN   reported        ON  reported.account_id = u.id
LEFT JOIN   assigned        ON  assigned.account_id = u.id
LEFT JOIN   commented       ON  commented.account_id = u.id
LEFT JOIN   attached        ON  attached.account_id = u.id
WHERE   u.kind = 'user'
ORDER BY u.username;

-- name: DeleteRecoveryCodes :exec
DELETE
FROM    recovery_codes
WHERE   principal_id = @principal_id;

-- name: InsertRecoveryCodes :exec
INSERT INTO recovery_codes
    (principal_id
    ,code_hash)
SELECT   @principal_id
        ,unnest(@code_hashes::BYTEA[]);

-- name: UseRecoveryCode :execrows
UPDATE  recovery_codes
SET     used_at = NOW()
WHERE   principal_id = @principal_id
        AND code_hash = @code_hash
        AND used_at IS NULL;

-- name: CountRecoveryCodes :one
SELECT  COUNT(*)
FROM    recovery_codes
WHERE   principal_id = @principal_id
        AND used_at IS NULL;

-- name: CreateResetLink :exec
INSERT INTO reset_links
    (token_hash
    ,principal_id
    ,expires_at)
VALUES
    (@token_hash
    ,@principal_id
    ,@expires_at);

-- name: GetResetLink :one
SELECT  sqlc.embed(p)
FROM    reset_links r
JOIN    principals  p   ON  p.id = r.principal_id
WHERE   r.token_hash = @token_hash
        AND r.used_at IS NULL
        AND r.expires_at > NOW()
        AND p.kind = 'user';

-- name: UseResetLinks :exec
-- Completing one link uses every open link for the user, so an older link
-- can't undo the new credentials.
UPDATE  reset_links
SET     used_at = NOW()
WHERE   principal_id = @principal_id
        AND used_at IS NULL;
