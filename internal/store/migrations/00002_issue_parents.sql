-- +goose Up
ALTER TABLE issues
     ADD COLUMN parent_id   BIGINT
    ,ADD        FOREIGN KEY (project_id, parent_id)
                REFERENCES issues (project_id, id);

CREATE INDEX issues_parent ON issues(parent_id) WHERE parent_id IS NOT NULL;
