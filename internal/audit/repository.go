package audit

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/safejson"
	"regexp"
)

type Entry struct {
	ActorID, ObjectID id.ID
	Action            string
	Details           []byte
}

var actionPattern = regexp.MustCompile(`^[a-z][a-z0-9_.]{1,95}$`)

func Append(ctx context.Context, tx pgx.Tx, e Entry) error {
	if !actionPattern.MatchString(e.Action) {
		return fmt.Errorf("invalid audit action")
	}
	details, err := safejson.Canonical(e.Details)
	if err != nil {
		return err
	}
	i, err := id.New()
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "INSERT INTO audit_logs(id,actor_id,action,object_id,details) VALUES($1,$2,$3,$4,$5)", i, nullable(e.ActorID), e.Action, nullable(e.ObjectID), details)
	return err
}
func nullable(i id.ID) any {
	if i == "" {
		return nil
	}
	return i
}
