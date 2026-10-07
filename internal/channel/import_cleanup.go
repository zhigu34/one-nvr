package channel

import "context"

// Preserve frozen input until the accepted parent reaches its terminal receipt.
// An abandoned/finished preview retains safe summaries, never encrypted camera
// input indefinitely after its thirty-minute admission window.
func (s *SourceService) CleanupExpiredImports(ctx context.Context) error {
	_, err := s.DB.Pool.Exec(ctx, `UPDATE source_import_items i SET draft_nonce=NULL,draft_ciphertext=NULL,credential_key_id=NULL FROM source_import_batches b WHERE i.batch_id=b.id AND b.expires_at<=clock_timestamp() AND i.draft_ciphertext IS NOT NULL AND NOT EXISTS(SELECT 1 FROM jobs j WHERE j.id=b.job_id AND j.state IN ('queued','running'))`)
	return err
}
