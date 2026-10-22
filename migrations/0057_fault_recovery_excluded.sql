-- A fault can end because the requirement went away, not because it was fixed.
--
-- Two endings were expressible: 'auto' (the stream came back) and 'manual' (an
-- operator confirmed the cause). Neither covers "recording was switched off, so
-- this kind is no longer required to run" — and until now that case could not
-- close a fault at all, because the resolver only ran on a healthy sample.
--
-- Measured on the live site: CH02's recording fault opened on 2026-10-18 and was
-- still open four days later, while the channel's main stream was fine and the
-- overview showed 中断中 beside it. The configuration had changed; the fault had
-- not been told.
--
-- 'excluded' is its own value rather than reusing 'manual': no human closed it,
-- and a report that counts policy changes as operator interventions would be
-- wrong about who did what.
ALTER TABLE fault_events DROP CONSTRAINT IF EXISTS fault_events_recovery_check;
ALTER TABLE fault_events ADD CONSTRAINT fault_events_recovery_check
  CHECK (recovery IS NULL OR recovery IN ('auto','manual','excluded'));

-- Close the rows this defect left open. They are identified by the interval that
-- followed them: an excluded or healthy recording state exists after the fault
-- started, which is what the resolver should have done at the time.
--
-- The end of each is the first such observation, not now(): the fault ended when
-- the requirement went away, and stamping the migration's run time would invent
-- four days of downtime.
WITH ended AS (
  SELECT f.id,
         f.started_at,
         (SELECT i.started_at
            FROM reliability_intervals i
           WHERE i.channel_id = f.channel_id
             AND i.kind = f.kind
             AND i.started_at > f.started_at
             AND (i.state = 'healthy' OR i.state = 'excluded')
           ORDER BY i.started_at
           LIMIT 1) AS resolved_at
    FROM fault_events f
   WHERE f.state = 'open'
)
UPDATE fault_events f
   SET state = 'resolved',
       ended_at = e.resolved_at,
       duration_ms = GREATEST(0, EXTRACT(EPOCH FROM (e.resolved_at - e.started_at)) * 1000)::bigint,
       recovery = 'excluded',
       updated_at = clock_timestamp()
  FROM ended e
 WHERE f.id = e.id
   AND e.resolved_at IS NOT NULL;
