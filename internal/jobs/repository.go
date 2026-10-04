package jobs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/database"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/safejson"
	"regexp"
	"time"
)

var ErrIdempotencyConflict = errors.New("idempotency key reused with different parameters")
var ErrLeaseLost = errors.New("job lease lost")
var ErrNoJob = errors.New("no eligible job")
var kindPattern = regexp.MustCompile(`^[a-z][a-z0-9_.]{1,95}$`)

type Input struct {
	Kind           string
	ObjectID       id.ID
	IdempotencyKey string
	Payload        []byte
}
type Lease struct {
	ID           id.ID
	Kind         string
	Attempt      int
	FencingToken id.ID
	ExpiresAt    time.Time
	Payload      []byte
}
type Result struct{ Payload []byte }
type Repository struct{ DB *database.DB }

func nullable(i id.ID) any {
	if i == "" {
		return nil
	}
	return i
}
func Enqueue(ctx context.Context, tx pgx.Tx, in Input) (id.ID, error) {
	if !kindPattern.MatchString(in.Kind) || len(in.IdempotencyKey) < 1 || len(in.IdempotencyKey) > 128 {
		return "", fmt.Errorf("invalid job kind or idempotency key")
	}
	payload, err := safejson.Canonical(in.Payload)
	if err != nil {
		return "", err
	}
	canonical, _ := json.Marshal(struct {
		Kind     string
		ObjectID id.ID
		Payload  json.RawMessage
	}{in.Kind, in.ObjectID, payload})
	hash := sha256.Sum256(canonical)
	i, err := id.New()
	if err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO jobs(id,kind,object_id,idempotency_key,parameter_digest,payload) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(kind,idempotency_key) DO NOTHING", i, in.Kind, nullable(in.ObjectID), in.IdempotencyKey, hash[:], payload); err != nil {
		return "", err
	}
	var existing id.ID
	var previous []byte
	if err = tx.QueryRow(ctx, "SELECT id,parameter_digest FROM jobs WHERE kind=$1 AND idempotency_key=$2", in.Kind, in.IdempotencyKey).Scan(&existing, &previous); err != nil {
		return "", err
	}
	if !bytes.Equal(hash[:], previous) {
		return "", ErrIdempotencyConflict
	}
	return existing, nil
}
func (r Repository) Claim(ctx context.Context, kind string) (Lease, error) {
	var lease Lease
	token, err := id.New()
	if err != nil {
		return lease, err
	}
	err = r.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		// Ordinary jobs have bounded retries. TLS has durable external intent:
		// retain reconciliation until its handler verifies and commits a terminal
		// domain result, even after a last-attempt crash. Backoff remains bounded.
		// Terminalize ordinary exhausted jobs even when no job is claimable.
		if _, err := tx.Exec(ctx, `WITH exhausted AS (
   SELECT id FROM jobs WHERE kind <> 'tls.apply' AND state='running' AND attempt>=max_attempts AND lease_expires_at<=clock_timestamp()
   FOR UPDATE SKIP LOCKED
  ), terminal AS (
   UPDATE jobs SET state='failed',error_code='lease_expired',lease_expires_at=NULL,updated_at=clock_timestamp()
   FROM exhausted WHERE jobs.id=exhausted.id RETURNING jobs.id
  ) UPDATE job_attempts SET state='lease_expired',error_code='lease_expired',finished_at=clock_timestamp()
  WHERE state='running' AND job_id IN (SELECT id FROM terminal)`); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `WITH candidate AS (
   SELECT id FROM jobs WHERE kind=$1 AND (kind='tls.apply' OR attempt<max_attempts) AND
    ((state='queued' AND available_at<=clock_timestamp()) OR (state='running' AND lease_expires_at<=clock_timestamp()))
   ORDER BY available_at,created_at,id FOR UPDATE SKIP LOCKED LIMIT 1
  ) UPDATE jobs SET state='running',attempt=attempt+1,fencing_token=$2,
   lease_expires_at=clock_timestamp()+interval '30 seconds',updated_at=clock_timestamp()
  FROM candidate WHERE jobs.id=candidate.id RETURNING jobs.id,jobs.kind,jobs.attempt,jobs.fencing_token,jobs.lease_expires_at,jobs.payload`, kind, token).Scan(&lease.ID, &lease.Kind, &lease.Attempt, &lease.FencingToken, &lease.ExpiresAt, &lease.Payload)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "UPDATE job_attempts SET state='lease_expired',finished_at=clock_timestamp() WHERE job_id=$1 AND state='running'", lease.ID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "INSERT INTO job_attempts(job_id,attempt,fencing_token,state) VALUES($1,$2,$3,'running')", lease.ID, lease.Attempt, token)
		return err
	})
	if err == nil && lease.ID == "" {
		err = ErrNoJob
	}
	return lease, err
}
func (r Repository) Renew(ctx context.Context, l Lease) error {
	tag, err := r.DB.Pool.Exec(ctx, `UPDATE jobs SET lease_expires_at=clock_timestamp()+interval '30 seconds',updated_at=clock_timestamp() WHERE id=$1 AND fencing_token=$2 AND attempt=$3 AND state='running' AND lease_expires_at>clock_timestamp()`, l.ID, l.FencingToken, l.Attempt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	return nil
}
func (r Repository) Complete(ctx context.Context, l Lease, result Result) error {
	payload, err := safejson.Canonical(result.Payload)
	if err != nil {
		return err
	}
	return r.finish(ctx, l, payload, "", false)
}
func (r Repository) Fail(ctx context.Context, l Lease, code string) error {
	if !kindPattern.MatchString(code) {
		return fmt.Errorf("invalid error code")
	}
	return r.finish(ctx, l, nil, code, false)
}
func (r Repository) FailPermanent(ctx context.Context, l Lease, code string) error {
	if !kindPattern.MatchString(code) {
		return fmt.Errorf("invalid error code")
	}
	return r.finish(ctx, l, nil, code, true)
}
func (r Repository) finish(ctx context.Context, l Lease, payload []byte, code string, permanent bool) error {
	return r.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		state := "succeeded"
		if code != "" {
			state = "queued"
		}
		delay := backoff(l.Attempt)
		var actual string
		err := tx.QueryRow(ctx, `UPDATE jobs SET state=CASE WHEN $4='queued' AND ((kind <> 'tls.apply' AND attempt>=max_attempts) OR $8) THEN 'failed' ELSE $4 END,
   result=$5,error_code=NULLIF($6,''),available_at=clock_timestamp()+make_interval(secs=>$7),
   lease_expires_at=NULL,updated_at=clock_timestamp()
   WHERE id=$1 AND fencing_token=$2 AND attempt=$3 AND state='running' AND lease_expires_at>clock_timestamp() RETURNING state`, l.ID, l.FencingToken, l.Attempt, state, payload, code, delay, permanent).Scan(&actual)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrLeaseLost
		}
		if err != nil {
			return err
		}
		attemptState := actual
		if actual == "queued" {
			attemptState = "retry"
		}
		_, err = tx.Exec(ctx, "UPDATE job_attempts SET state=$3,finished_at=clock_timestamp(),error_code=NULLIF($4,'') WHERE job_id=$1 AND fencing_token=$2", l.ID, l.FencingToken, attemptState, code)
		return err
	})
}
func backoff(attempt int) int {
	switch attempt {
	case 1:
		return 2
	case 2:
		return 4
	case 3:
		return 8
	case 4:
		return 16
	default:
		return 30
	}
}
