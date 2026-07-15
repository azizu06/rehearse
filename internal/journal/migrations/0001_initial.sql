CREATE TABLE plans (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE plan_versions (
    plan_id TEXT NOT NULL,
    version INTEGER NOT NULL CHECK (version > 0),
    source_kind TEXT NOT NULL,
    target_kind TEXT NOT NULL,
    credential_references TEXT NOT NULL CHECK (json_valid(credential_references)),
    created_at TEXT NOT NULL,
    PRIMARY KEY (plan_id, version),
    FOREIGN KEY (plan_id) REFERENCES plans(id) ON DELETE RESTRICT
);

CREATE TABLE runs (
    id TEXT PRIMARY KEY,
    plan_id TEXT NOT NULL,
    plan_version INTEGER NOT NULL,
    stage TEXT NOT NULL CHECK (stage IN ('queued', 'preflight', 'acquire', 'restore', 'boot', 'probe', 'report', 'cleanup')),
    outcome TEXT NOT NULL DEFAULT '' CHECK (outcome IN ('', 'succeeded', 'failed', 'cancelled', 'timed_out')),
    cleanup_status TEXT NOT NULL CHECK (cleanup_status IN ('not_started', 'pending', 'succeeded', 'failed')),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    version INTEGER NOT NULL CHECK (version > 0),
    needs_reconciliation INTEGER NOT NULL DEFAULT 0 CHECK (needs_reconciliation IN (0, 1)),
    reconciliation_requested_at TEXT NOT NULL DEFAULT '',
    FOREIGN KEY (plan_id, plan_version) REFERENCES plan_versions(plan_id, version) ON DELETE RESTRICT
);

CREATE TABLE run_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id TEXT NOT NULL,
    sequence INTEGER NOT NULL CHECK (sequence > 0),
    kind TEXT NOT NULL CHECK (kind IN (
        'run_created',
        'stage_started',
        'run_succeeded',
        'run_failed',
        'run_cancelled',
        'run_timed_out',
        'cleanup_succeeded',
        'cleanup_failed',
        'reconciliation_required'
    )),
    stage TEXT NOT NULL CHECK (stage IN ('queued', 'preflight', 'acquire', 'restore', 'boot', 'probe', 'report', 'cleanup')),
    outcome TEXT NOT NULL DEFAULT '' CHECK (outcome IN ('', 'succeeded', 'failed', 'cancelled', 'timed_out')),
    cleanup_status TEXT NOT NULL CHECK (cleanup_status IN ('not_started', 'pending', 'succeeded', 'failed')),
    occurred_at TEXT NOT NULL,
    UNIQUE (run_id, sequence),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE RESTRICT
);

CREATE INDEX runs_reconciliation_idx ON runs(needs_reconciliation, cleanup_status);
CREATE INDEX run_events_run_sequence_idx ON run_events(run_id, sequence);

CREATE TRIGGER run_events_are_immutable_on_update
BEFORE UPDATE ON run_events
BEGIN
    SELECT RAISE(ABORT, 'run events are immutable');
END;

CREATE TRIGGER run_events_are_immutable_on_delete
BEFORE DELETE ON run_events
BEGIN
    SELECT RAISE(ABORT, 'run events are immutable');
END;
