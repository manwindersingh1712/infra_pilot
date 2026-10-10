package store_test

import (
	"context"
	"testing"

	"infrapilot/internal/store"
	"infrapilot/internal/testutil"
)

func TestReplaceEnvVarIfEquals(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	f := testutil.Seed(t, st, store.TypeRedis)
	if _, err := st.UpsertEnvVar(ctx, f.ServiceID, "K", "old"); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.ReplaceEnvVarIfEquals(ctx, f.ServiceID, "K", "other", "new"); err != nil || ok {
		t.Fatalf("mismatch must not replace: ok=%v err=%v", ok, err)
	}
	if ok, err := st.ReplaceEnvVarIfEquals(ctx, f.ServiceID, "K", "old", "new"); err != nil || !ok {
		t.Fatalf("match must replace: ok=%v err=%v", ok, err)
	}
	if ok, _ := st.ReplaceEnvVarIfEquals(ctx, f.ServiceID, "K", "old", "again"); ok {
		t.Fatal("a second swap against the stale value must lose")
	}
	m, _ := st.EnvMap(ctx, f.ServiceID)
	if m["K"] != "new" {
		t.Fatalf("K = %q", m["K"])
	}
	if ok, err := st.ReplaceEnvVarIfEquals(ctx, f.ServiceID, "MISSING", "", "x"); err != nil || ok {
		t.Fatalf("absent key: ok=%v err=%v", ok, err)
	}
}
