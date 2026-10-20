-- Ring-buffer fragments were recorded as unregistered recording runs. PRD REC-09.
--
-- Every 3-second fragment of every buffering channel produces a completion
-- callback. None of them is a run this index knows about — the fragments are
-- disposable by design and only the assembled clip is published — so each was
-- written as a diagnostic row. Measured on the live site: 145,019 rows and 60 MB
-- of payload in two days, about 5,400 an hour, which is sixteen channels at one
-- fragment per 3.3 seconds.
--
-- The code now accepts those callbacks without writing anything, so the growth has
-- stopped. The seven-day retention on hook_inbox cannot clear the backlog: it only
-- removes rows older than a week, and every one of these is younger than that.
--
-- The condition names the SOURCE (a file path on the buffer mount), not the row
-- age, so a real anomaly — a completion for a path outside the buffer with no
-- matching run — is untouched even if it shares the error code.
DELETE FROM hook_inbox
WHERE state = 'diagnostic'
  AND error_code = 'unregistered_recording_run'
  AND payload ->> 'FilePath' LIKE '/cache/%';
