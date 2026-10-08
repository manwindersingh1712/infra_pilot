package main

import (
	"bytes"
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
