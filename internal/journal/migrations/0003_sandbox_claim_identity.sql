ALTER TABLE sandbox_cleanup_claims
ADD COLUMN claim_id TEXT NOT NULL DEFAULT '';

CREATE INDEX sandbox_cleanup_claims_claim_idx
ON sandbox_cleanup_claims(status, claim_id, run_id);
