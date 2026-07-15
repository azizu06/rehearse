CREATE TABLE sandbox_cleanup_claims (
    run_id TEXT PRIMARY KEY,
    status TEXT NOT NULL CHECK (status IN ('pending', 'failed', 'succeeded')),
    claimed_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE RESTRICT
);

CREATE INDEX sandbox_cleanup_claims_status_idx
ON sandbox_cleanup_claims(status, run_id);
