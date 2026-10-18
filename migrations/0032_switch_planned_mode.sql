-- M5 recording batch 2, correction: allow 'planned' on the switch row.
-- PRD REC-02, REC-04.
--
-- 0005 declared source_switches.desired_mode as ('none','continuous'), with
-- old_mode already accepting ('none','continuous','planned') — a mode that could
-- be recorded as *old* but never requested as *new*. That asymmetry was
-- harmless while nothing wrote 'planned'. Now that an operator can apply it, the
-- constraint rejects the switch before any work happens, and the API surfaces it
-- as a bare 503 because a SQLSTATE is not a recognised fault.
--
-- Widening a CHECK is safe in place: every existing row already satisfies it, so
-- no backfill and no table rewrite is implied.

ALTER TABLE source_switches
  DROP CONSTRAINT IF EXISTS source_switches_desired_mode_check;

ALTER TABLE source_switches
  ADD CONSTRAINT source_switches_desired_mode_check
  CHECK (desired_mode IS NULL OR desired_mode IN ('none','continuous','planned'));
