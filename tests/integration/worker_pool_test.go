package integration

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zhigu34/one-nvr/internal/app"
	"testing"
	"time"
)

func TestWorkerPoolLeavesCapacityForTLSAndLeases(t *testing.T) {
	// A two-core CI/low-power host otherwise receives pgxpool's four-connection
	// default, all of which the recording monitor may own simultaneously.
	c, err := pgxpool.ParseConfig(testDB(t).Pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	c.MaxConns = 4
	app.ConfigureDatabasePool(c, "worker")
	db, err := pgxpool.NewWithConfig(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	held := []*pgxpool.Conn{}
	defer func() {
		for _, conn := range held {
			conn.Release()
		}
	}()
	// Bounded owners: four runtime checks, two tests, four source queues and five
	// possible nested/idle pool probes. Ordinary queue/TLS/lease DB work must fit.
	for i := 0; i < 15; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		conn, err := db.Acquire(ctx)
		cancel()
		if err != nil {
			t.Fatal("media ownership exhausted application DB pool", i, err)
		}
		held = append(held, conn)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var one int
	if err := db.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Fatal("TLS/lease work starved by media ownership", err)
	}
}
