-- Storage write admission is a reliability setting, not a correctness one: a
-- site may accept that a pool started recording before its first real MP4 was
-- proven, and rely on the runtime watchdog to stop a recorder that produces
-- nothing. The default keeps the existing behaviour (evidence required), so no
-- existing site changes behaviour and the accepted M1-B admission contract
-- stays the default. The column only governs admission-time proof; the running
-- capacity gate and the "stop recording when nothing lands" watchdog are not
-- optional at any value.
ALTER TABLE sites ADD COLUMN require_storage_write_proof boolean NOT NULL DEFAULT true;
