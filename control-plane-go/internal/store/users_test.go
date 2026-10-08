package store_test

import (
	"context"
	"errors"
	"testing"

	"infrapilot/internal/store"
)

func TestCreateAndGetUser(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	name := "Ada"
	u, err := st.CreateUser(ctx, "ada@example.com", "hash", &name)
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.GetUserByEmail(ctx, "ada@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != u.ID || got.Password != "hash" || got.Name == nil || *got.Name != "Ada" {
		t.Fatalf("got %+v", got)
	}
}

func TestCreateUserDuplicateEmail(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	if _, err := st.CreateUser(ctx, "a@b.c", "h", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser(ctx, "a@b.c", "h", nil); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}

func TestGetUserByEmailNotFound(t *testing.T) {
	st := newStore(t)
	if _, err := st.GetUserByEmail(context.Background(), "nobody@x.y"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestUpsertDevUserIsIdempotent(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	a, err := st.UpsertDevUser(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.UpsertDevUser(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != b.ID || a.Email != "dev@local" {
		t.Fatalf("a=%+v b=%+v", a, b)
	}
}
