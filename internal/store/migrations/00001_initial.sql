-- +goose Up
CREATE TABLE principals (
     id                 BIGINT                  GENERATED ALWAYS AS IDENTITY
                                                PRIMARY KEY
    ,kind               TEXT        NOT NULL    CHECK (kind IN ('user', 'agent'))
    ,username           TEXT        NOT NULL    UNIQUE
    ,display_name       TEXT        NOT NULL
    ,email              TEXT        NOT NULL    DEFAULT ''
    ,owner_id           BIGINT                  REFERENCES principals (id)
    ,site_admin         BOOLEAN     NOT NULL    DEFAULT FALSE
    ,password_hash      TEXT
    ,totp_secret        BYTEA
    ,totp_last_step     BIGINT      NOT NULL    DEFAULT 0
    ,session_version    INTEGER     NOT NULL    DEFAULT 1
    ,disabled_at        TIMESTAMPTZ
    ,created_at         TIMESTAMPTZ NOT NULL    DEFAULT NOW()
    ,last_sign_in_at    TIMESTAMPTZ

    ,CHECK ((kind = 'agent') = (owner_id IS NOT NULL))
);

CREATE INDEX principals_owner ON principals(owner_id) WHERE owner_id IS NOT NULL;

CREATE TABLE recovery_codes (
     id             BIGINT              GENERATED ALWAYS AS IDENTITY
                                        PRIMARY KEY
    ,principal_id   BIGINT  NOT NULL    REFERENCES principals (id)
                                        ON DELETE CASCADE
    ,code_hash      BYTEA   NOT NULL
    ,used_at        TIMESTAMPTZ
);

CREATE INDEX recovery_codes_principal ON recovery_codes(principal_id);

CREATE TABLE reset_links (
     token_hash     BYTEA                   PRIMARY KEY
    ,principal_id   BIGINT      NOT NULL    REFERENCES principals (id)
                                            ON DELETE CASCADE
    ,expires_at     TIMESTAMPTZ NOT NULL
    ,used_at        TIMESTAMPTZ
);

CREATE INDEX reset_links_principal ON reset_links(principal_id);

CREATE TABLE projects (
     id             BIGINT                  GENERATED ALWAYS AS IDENTITY ( START WITH 10000 )
                                            PRIMARY KEY
    ,key            TEXT        NOT NULL
    ,name           TEXT        NOT NULL
    ,next_number    INTEGER     NOT NULL    DEFAULT 1
    ,created_at     TIMESTAMPTZ NOT NULL    DEFAULT NOW()
);

CREATE TABLE memberships (
     id             BIGINT                  GENERATED ALWAYS AS IDENTITY
                                            PRIMARY KEY
    ,project_id     BIGINT      NOT NULL    REFERENCES projects (id)
                                            ON DELETE CASCADE
    ,principal_id   BIGINT      NOT NULL    REFERENCES principals (id)
    ,role           TEXT        NOT NULL    CHECK (role IN ('admin', 'member'))
    ,key            TEXT        NOT NULL
    ,created_at     TIMESTAMPTZ NOT NULL    DEFAULT NOW()
    ,removed_at     TIMESTAMPTZ

    ,UNIQUE (project_id, principal_id)
    ,UNIQUE (project_id, id)
);

CREATE UNIQUE INDEX memberships_key ON memberships(principal_id, key) WHERE removed_at IS NULL;

CREATE TABLE statuses (
     id         INTEGER             PRIMARY KEY
    ,name       TEXT    NOT NULL    UNIQUE
    ,category   TEXT    NOT NULL    CHECK (category IN ('new', 'indeterminate', 'done'))
    ,position   INTEGER NOT NULL
);

CREATE TABLE issue_types (
     id             INTEGER             PRIMARY KEY
    ,name           TEXT    NOT NULL    UNIQUE
    ,description    TEXT    NOT NULL
    ,position       INTEGER NOT NULL
);

CREATE TABLE priorities (
     id         INTEGER             PRIMARY KEY
    ,name       TEXT    NOT NULL    UNIQUE
    ,position   INTEGER NOT NULL
);

INSERT INTO statuses
    (id
    ,name
    ,category
    ,position)
VALUES
     (1, 'To Do', 'new', 1)
    ,(2, 'In Progress', 'indeterminate', 2)
    ,(3, 'In Review', 'indeterminate', 3)
    ,(4, 'Blocked', 'indeterminate', 4)
    ,(5, 'Done', 'done', 5)
    ,(6, 'Cancelled', 'done', 6);

INSERT INTO issue_types
    (id
    ,name
    ,description
    ,position)
VALUES
     (10001, 'Task', 'A piece of work', 1)
    ,(10002, 'Bug', 'Something is broken', 2)
    ,(10003, 'Story', 'A user-facing change', 3)
    ,(10004, 'Epic', 'A body of related work', 4)
    ,(10005, 'Chore', 'Maintenance work', 5);

INSERT INTO priorities
    (id
    ,name
    ,position)
VALUES
     (1, 'Highest', 1)
    ,(2, 'High', 2)
    ,(3, 'Medium', 3)
    ,(4, 'Low', 4)
    ,(5, 'Lowest', 5);

CREATE TABLE issues (
     id             BIGINT                  GENERATED ALWAYS AS IDENTITY ( START WITH 100000 )
                                            PRIMARY KEY
    ,project_id     BIGINT      NOT NULL    REFERENCES projects (id)
                                            ON DELETE CASCADE
    ,number         INTEGER     NOT NULL
    ,summary        TEXT        NOT NULL
    ,description    TEXT        NOT NULL    DEFAULT ''
    ,status_id      INTEGER     NOT NULL    REFERENCES statuses (id)
    ,type_id        INTEGER     NOT NULL    REFERENCES issue_types (id)
    ,priority_id    INTEGER                 REFERENCES priorities (id)
    ,reporter_id    BIGINT      NOT NULL
    ,assignee_id    BIGINT
    ,labels         TEXT[]      NOT NULL    DEFAULT '{}'
    ,created_at     TIMESTAMPTZ NOT NULL    DEFAULT NOW()
    ,updated_at     TIMESTAMPTZ NOT NULL    DEFAULT NOW()

    ,UNIQUE (project_id, number)
    ,UNIQUE (project_id, id)
    ,FOREIGN KEY (project_id, reporter_id)
     REFERENCES memberships (project_id, id)
    ,FOREIGN KEY (project_id, assignee_id)
     REFERENCES memberships (project_id, id)
);

CREATE INDEX issues_reporter ON issues(reporter_id);

CREATE INDEX issues_assignee ON issues(assignee_id) WHERE assignee_id IS NOT NULL;

CREATE TABLE comments (
     id         BIGINT                  GENERATED ALWAYS AS IDENTITY ( START WITH 10000 )
                                        PRIMARY KEY
    ,project_id BIGINT      NOT NULL
    ,issue_id   BIGINT      NOT NULL
    ,author_id  BIGINT      NOT NULL
    ,body       TEXT        NOT NULL
    ,created_at TIMESTAMPTZ NOT NULL    DEFAULT NOW()
    ,updated_at TIMESTAMPTZ NOT NULL    DEFAULT NOW()

    ,FOREIGN KEY (project_id, issue_id)
     REFERENCES issues (project_id, id)
     ON DELETE CASCADE
    ,FOREIGN KEY (project_id, author_id)
     REFERENCES memberships (project_id, id)
);

CREATE INDEX comments_issue ON comments(issue_id, created_at);

CREATE INDEX comments_author ON comments(author_id);

CREATE TABLE attachments (
     id             BIGINT                  GENERATED ALWAYS AS IDENTITY ( START WITH 10000 )
                                            PRIMARY KEY
    ,project_id     BIGINT      NOT NULL
    ,issue_id       BIGINT      NOT NULL
    ,uploader_id    BIGINT      NOT NULL
    ,filename       TEXT        NOT NULL
    ,mime_type      TEXT        NOT NULL
    ,size           BIGINT      NOT NULL
    ,sha256         BYTEA       NOT NULL
    ,created_at     TIMESTAMPTZ NOT NULL    DEFAULT NOW()

    ,FOREIGN KEY (project_id, issue_id)
     REFERENCES issues (project_id, id)
     ON DELETE CASCADE
    ,FOREIGN KEY (project_id, uploader_id)
     REFERENCES memberships (project_id, id)
);

CREATE INDEX attachments_issue ON attachments(issue_id, created_at);

CREATE INDEX attachments_uploader ON attachments(uploader_id);

CREATE TABLE issue_history (
     id         BIGINT                  GENERATED ALWAYS AS IDENTITY
                                        PRIMARY KEY
    ,project_id BIGINT      NOT NULL
    ,issue_id   BIGINT      NOT NULL
    ,actor_id   BIGINT      NOT NULL
    ,field      TEXT        NOT NULL
    ,old_value  TEXT
    ,new_value  TEXT
    ,created_at TIMESTAMPTZ NOT NULL    DEFAULT NOW()

    ,FOREIGN KEY (project_id, issue_id)
     REFERENCES issues (project_id, id)
     ON DELETE CASCADE
    ,FOREIGN KEY (project_id, actor_id)
     REFERENCES memberships (project_id, id)
);

CREATE INDEX issue_history_issue ON issue_history(issue_id, created_at);

CREATE INDEX issue_history_actor ON issue_history(actor_id);

CREATE TABLE api_keys (
     id             BIGINT                  GENERATED ALWAYS AS IDENTITY
                                            PRIMARY KEY
    ,principal_id   BIGINT      NOT NULL    REFERENCES principals (id)
    ,name           TEXT        NOT NULL
    ,key_hash       BYTEA       NOT NULL    UNIQUE
    ,read_only      BOOLEAN     NOT NULL
    ,all_projects   BOOLEAN     NOT NULL
    ,created_at     TIMESTAMPTZ NOT NULL    DEFAULT NOW()
    ,expires_at     TIMESTAMPTZ
    ,revoked_at     TIMESTAMPTZ
    ,last_used_at   TIMESTAMPTZ
);

CREATE INDEX api_keys_principal ON api_keys(principal_id);

CREATE TABLE api_key_projects (
     api_key_id BIGINT  NOT NULL    REFERENCES api_keys (id)
                                    ON DELETE CASCADE
    ,project_id BIGINT  NOT NULL    REFERENCES projects (id)
                                    ON DELETE CASCADE

    ,PRIMARY KEY (api_key_id, project_id)
);

CREATE INDEX api_key_projects_project ON api_key_projects(project_id);

CREATE TABLE sessions (
     token  TEXT                    PRIMARY KEY
    ,data   BYTEA       NOT NULL
    ,expiry TIMESTAMPTZ NOT NULL
);

CREATE INDEX sessions_expiry ON sessions(expiry);
