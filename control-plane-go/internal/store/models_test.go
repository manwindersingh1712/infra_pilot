package store

import (
	"encoding/json"
	"testing"
	"time"
)

func TestTimestampMarshalsLikeJavaScriptToISOString(t *testing.T) {
	ts := Timestamp{time.Date(2026, 1, 2, 3, 4, 5, 120_000_000, time.UTC)}
	b, err := json.Marshal(ts)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `"2026-01-02T03:04:05.120Z"` {
		t.Fatalf("got %s", b)
	}
}

func TestTimestampConvertsToUTC(t *testing.T) {
	ist := time.FixedZone("IST", 5*3600+1800)
	ts := Timestamp{time.Date(2026, 1, 2, 9, 0, 0, 0, ist)}
	b, _ := json.Marshal(ts)
	if string(b) != `"2026-01-02T03:30:00.000Z"` {
		t.Fatalf("got %s", b)
	}
}

func TestServiceTypeHelpers(t *testing.T) {
	if !IsManaged("redis") || !IsManaged("mongodb") || IsManaged("nodejs") {
		t.Fatal("IsManaged wrong")
	}
	for _, typ := range []string{"docker", "nodejs", "nextjs", "react"} {
		if !NeedsRepo(typ) {
			t.Fatalf("NeedsRepo(%q) = false", typ)
		}
	}
	if NeedsRepo("redis") || !ValidServiceType("nextjs") || ValidServiceType("php") {
		t.Fatal("service type helpers wrong")
	}
}
