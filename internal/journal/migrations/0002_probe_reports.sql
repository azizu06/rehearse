ALTER TABLE plan_versions
ADD COLUMN probe_config TEXT NOT NULL DEFAULT '';

CREATE TABLE run_reports (
    run_id TEXT PRIMARY KEY,
    schema_version TEXT NOT NULL,
    document TEXT NOT NULL CHECK (json_valid(document)),
    created_at TEXT NOT NULL,
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE RESTRICT
);

CREATE TRIGGER run_reports_are_immutable_on_update
BEFORE UPDATE ON run_reports
BEGIN
    SELECT RAISE(ABORT, 'run reports are immutable');
END;

CREATE TRIGGER run_reports_are_immutable_on_delete
BEFORE DELETE ON run_reports
BEGIN
    SELECT RAISE(ABORT, 'run reports are immutable');
END;
