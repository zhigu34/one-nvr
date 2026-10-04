CREATE TABLE gateway_apply_results (
 job_id uuid PRIMARY KEY REFERENCES jobs(id),
 result jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
