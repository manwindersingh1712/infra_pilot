package db_test

import (
	"testing"

	"infrapilot/internal/testutil"
)

// adminURL is the throwaway server's admin database; each integration test
// creates its own database on it with testutil.FreshDB. Empty under -short.
var adminURL string

func TestMain(m *testing.M) { testutil.RunWithEmptyPostgres(m, &adminURL) }
