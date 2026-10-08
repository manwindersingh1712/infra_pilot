package store_test

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"infrapilot/internal/store"
	"infrapilot/internal/testutil"
)

var pool *pgxpool.Pool

func TestMain(m *testing.M) { testutil.RunWithPostgres(m, &pool) }

func newStore(t *testing.T) *store.Store {
	t.Helper()
	return store.New(testutil.NeedDB(t, pool))
}
