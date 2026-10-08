package testutil

import (
	"context"
	"testing"

	"github.com/lucsky/cuid"

	"infrapilot/internal/store"
)

// Fixture holds the IDs created by Seed.
type Fixture struct {
	UserID    string
	ProjectID string
	ServiceID string
}

// Seed creates a user, a project named "proj" and a service named "svc" of the given type.
func Seed(t *testing.T, st *store.Store, serviceType string) Fixture {
	t.Helper()
	ctx := context.Background()
	u, err := st.CreateUser(ctx, cuid.New()+"@test.local", "hash", nil)
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	p, err := st.CreateProject(ctx, "proj", u.ID)
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}
	var repoURL *string
	if store.NeedsRepo(serviceType) {
		repo := "https://github.com/example/app.git"
		repoURL = &repo
	}
	svc, err := st.CreateService(ctx, store.NewService{
		ProjectID: p.ID, Name: "svc", ServiceType: serviceType, RepoURL: repoURL, Branch: "main",
	})
	if err != nil {
		t.Fatalf("seed service: %v", err)
	}
	return Fixture{UserID: u.ID, ProjectID: p.ID, ServiceID: svc.ID}
}
