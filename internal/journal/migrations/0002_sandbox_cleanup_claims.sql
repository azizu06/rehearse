CREATE TABLE sandbox_cleanup_claims (
    run_id TEXT PRIMARY KEY,
    claim_id TEXT NOT NULL
        CHECK (length(claim_id) = 64 AND claim_id NOT GLOB '*[^0-9a-f]*'),
    status TEXT NOT NULL CHECK (status IN ('pending', 'failed', 'succeeded')),
    claimed_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (run_id, claim_id),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE RESTRICT
);

CREATE INDEX sandbox_cleanup_claims_status_idx
ON sandbox_cleanup_claims(status, claim_id, run_id);

CREATE TABLE sandbox_cleanup_resources (
    run_id TEXT NOT NULL,
    claim_id TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('container', 'network', 'volume')),
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 255),
    generation_id TEXT NOT NULL
        CHECK (length(generation_id) = 64 AND generation_id NOT GLOB '*[^0-9a-f]*'),
    expected_at TEXT NOT NULL,
    PRIMARY KEY (run_id, kind, name),
    FOREIGN KEY (run_id, claim_id)
        REFERENCES sandbox_cleanup_claims(run_id, claim_id) ON DELETE RESTRICT
) WITHOUT ROWID;

CREATE INDEX sandbox_cleanup_resources_claim_idx
ON sandbox_cleanup_resources(run_id, claim_id, kind, name);
