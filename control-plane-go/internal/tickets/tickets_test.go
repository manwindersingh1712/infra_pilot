package tickets_test

import (
	"context"
	"errors"
	"testing"
	"time"

	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"infrapilot/internal/tickets"
)

func TestIssueAndRedeemOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: needs Docker")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	ctr, err := tcredis.Run(ctx, "redis:7-alpine")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ctr.Terminate(context.Background()) })
	url, _ := ctr.ConnectionString(ctx)
	s, err := tickets.New(url, 60*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ticket, err := s.Issue(ctx, "user1:dep1")
	if err != nil || len(ticket) < 40 {
		t.Fatalf("ticket %q err %v", ticket, err)
	}
	other, _ := s.Issue(ctx, "user1:dep1")
	if other == ticket {
		t.Fatal("tickets must be unique")
	}
	if v, err := s.Redeem(ctx, ticket); err != nil || v != "user1:dep1" {
		t.Fatalf("redeem %q %v", v, err)
	}
	if _, err := s.Redeem(ctx, ticket); !errors.Is(err, tickets.ErrInvalid) {
		t.Fatalf("second redeem err = %v", err)
	}
	if _, err := s.Redeem(ctx, "made-up"); !errors.Is(err, tickets.ErrInvalid) {
		t.Fatalf("unknown ticket err = %v", err)
	}

	short, _ := tickets.New(url, 50*time.Millisecond)
	defer short.Close()
	expiring, _ := short.Issue(ctx, "v")
	time.Sleep(150 * time.Millisecond)
	if _, err := short.Redeem(ctx, expiring); !errors.Is(err, tickets.ErrInvalid) {
		t.Fatalf("expired ticket err = %v", err)
	}
}
