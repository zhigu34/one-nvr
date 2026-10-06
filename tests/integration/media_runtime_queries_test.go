package integration

import (
	"context"
	"testing"

	"github.com/zhigu34/one-nvr/internal/database"
)

// These exact queries are consumed by actual runtime and SIGKILL acceptance.
// Prepare against real migrations too, so API field names cannot silently
// replace storage columns in container-only code paths.
const runtimePublishedFilesQuery = `SELECT DISTINCT ON(c.id) p.canonical_path,l.relative_path,l.size_bytes FROM recording_locations l JOIN storage_pools p ON p.id=l.pool_id JOIN recording_segments s ON s.id=l.segment_id JOIN channels c ON c.id=s.channel_id WHERE c.channel_no IN (1,2) AND s.state='ready' ORDER BY c.id,s.ready_at DESC`
const runtimeFinalizingFileQuery = `SELECT s.id,s.run_id,s.source_revision_id,s.pool_id,p.canonical_path,s.original_relative_path,s.target_relative_path,s.naming_timezone FROM recording_segments s JOIN storage_pools p ON p.id=s.pool_id WHERE s.state='finalizing' AND s.original_relative_path=$1 AND s.target_relative_path=$2`
const runtimeCompletionCountsQuery = `SELECT count(*) FILTER (WHERE coalesce(h.payload->>'TimeEvidence','')=''),count(*) FILTER (WHERE h.payload->>'TimeEvidence'='recovered') FROM hook_inbox h JOIN recording_runs r ON r.id=h.run_id JOIN channels c ON c.id=r.channel_id WHERE c.channel_no IN (1,2)`

func TestRuntimeMediaQueriesMatchMigratedSchema(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, query string
		args        []any
	}{
		{name: "published files", query: runtimePublishedFilesQuery},
		{name: "interrupted finalizing identity", query: runtimeFinalizingFileQuery, args: []any{"fixture-original", "fixture-target"}},
		{name: "completion delivery diagnostics", query: runtimeCompletionCountsQuery},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := db.Pool.Query(ctx, tc.query, tc.args...)
			if err != nil {
				t.Fatalf("query rejected by real migrated schema: %v", err)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
