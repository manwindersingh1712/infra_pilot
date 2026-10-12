package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
)

func TestPrintStatus(t *testing.T) {
	at := time.Date(2026, 10, 8, 9, 30, 0, 0, time.UTC)
	statuses := []*goose.MigrationStatus{
		{Source: &goose.Source{Path: "00001_baseline.sql", Version: 1}, State: goose.StateApplied, AppliedAt: at},
		{Source: &goose.Source{Path: "00002_deployment_stopped_at.sql", Version: 2}, State: goose.StatePending},
	}
	var buf bytes.Buffer
	if err := printStatus(&buf, statuses); err != nil {
		t.Fatal(err)
	}
	want := "00001 00001_baseline.sql applied 2026-10-08T09:30:00Z\n" +
		"00002 00002_deployment_stopped_at.sql pending\n"
	if got := buf.String(); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRealMainExitCodes(t *testing.T) {
	t.Chdir(t.TempDir()) // no .env
	t.Setenv("ENV_ENCRYPTION_KEY", "")
	t.Setenv("DATABASE_URL", "postgres://u:hunter2@localhost:1/none")
	var out, errb bytes.Buffer
	if code := realMain(context.Background(), []string{"encrypt-env", "--yes"}, &out, &errb); code != 1 {
		t.Fatalf("missing key: code %d", code)
	}
	if strings.Contains(out.String()+errb.String(), "hunter2") {
		t.Fatal("password printed")
	}
	if code := realMain(context.Background(), []string{"bogus", "x"}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "usage: migrate up|status|baseline, or migrate encrypt-env [--dry-run]") {
		t.Fatalf("usage: code %d %q", code, errb.String())
	}
	// The usage line shows the encrypt-env flags only after encrypt-env.
	if u := errb.String(); strings.Contains(u, "up|status|baseline [") || strings.Contains(u, "baseline|encrypt-env") {
		t.Fatalf("flags shown for other commands: %q", u)
	}
}
