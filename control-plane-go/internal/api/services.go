package api

import (
	"errors"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"infrapilot/internal/gitsrc"
	"infrapilot/internal/store"
)

const (
	maskedValue  = "********"
	maxEnvKeyLen = 256
	maxEnvValue  = 32 << 10 // bytes; a PEM key is a few KiB
	maxLegacyKey = 1024     // longest key a delete will look up (Node-era rows were unchecked)
)

// envKeyPattern is what the worker accepts for a variable name (the 2026-10-10
// "relax env-var KEY validation" ruling): the API must not store a key the
// worker would then refuse to start the container with.
var envKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)

// publicService is the only way a service leaves the API: the repo URL goes
// through gitsrc.RedactURL so a token in the userinfo or query never reaches a
// response. Every handler that returns a service (create, list, canvas, ...)
// must use it.
func publicService(s store.Service) store.Service {
	if s.RepoURL != nil {
		redacted := gitsrc.RedactURL(*s.RepoURL)
		s.RepoURL = &redacted
	}
	return s
}

// ownedService writes 404 and returns false unless the path's {id} names a
// live service in a project the caller owns. A malformed, missing, deleted and
// foreign id all answer the same way.
func (s *Server) ownedService(w http.ResponseWriter, r *http.Request) (store.Service, bool) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "service_not_found")
		return store.Service{}, false
	}
	svc, err := s.Store.GetServiceForOwner(r.Context(), id, userID(r))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "service_not_found")
		return svc, false
	}
	if err != nil {
		s.internalError(w, r, err)
		return svc, false
	}
	return svc, true
}

// ownsProject writes 404 and returns false unless the caller owns the project.
// A malformed id answers like a missing one.
func (s *Server) ownsProject(w http.ResponseWriter, r *http.Request, projectID string) bool {
	if !idShape.MatchString(projectID) {
		writeError(w, http.StatusNotFound, "Project not found")
		return false
	}
	_, err := s.Store.GetProjectForOwner(r.Context(), projectID, userID(r))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "Project not found")
		return false
	}
	if err != nil {
		s.internalError(w, r, err)
		return false
	}
	return true
}

func (s *Server) createService(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ProjectID   string  `json:"projectId"`
		Name        string  `json:"name"`
		ServiceType *string `json:"serviceType"`
		RepoURL     *string `json:"repoUrl"`
		Branch      *string `json:"branch"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeDecodeError(w, err, "invalid_request")
		return
	}
	typ := store.TypeDocker
	if body.ServiceType != nil {
		typ = *body.ServiceType
	}
	branch := "main"
	if body.Branch != nil {
		branch = *body.Branch
	}
	switch {
	case body.ProjectID == "" || !validProjectName(body.Name) || !store.ValidServiceType(typ) ||
		gitsrc.ValidBranch(branch) != nil:
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	case body.RepoURL != nil && gitsrc.ValidRepoURL(*body.RepoURL) != nil:
		writeError(w, http.StatusBadRequest, "invalid_repo_url")
		return
	case store.NeedsRepo(typ) && body.RepoURL == nil:
		writeError(w, http.StatusBadRequest, "repoUrl required for docker/nodejs/nextjs/react services")
		return
	}
	if !s.ownsProject(w, r, body.ProjectID) {
		return
	}
	svc, err := s.Store.CreateService(r.Context(), store.NewService{
		ProjectID: body.ProjectID, Name: body.Name, ServiceType: typ, RepoURL: body.RepoURL, Branch: branch,
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, publicService(svc))
}

func (s *Server) listServices(w http.ResponseWriter, r *http.Request) {
	projectID := r.URL.Query().Get("projectId")
	if projectID != "" && !idShape.MatchString(projectID) {
		// Cannot be a stored id: same answer as a project with no services.
		writeJSON(w, http.StatusOK, []store.Service{})
		return
	}
	svcs, err := s.Store.ListServicesForOwner(r.Context(), userID(r), projectID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	for i := range svcs {
		svcs[i] = publicService(svcs[i])
	}
	writeJSON(w, http.StatusOK, svcs)
}

// deployService records a queued deployment and its outbox event atomically.
// Managed services skip the build (the workflow decides; the event type is
// kept for compatibility with Node-era rows).
func (s *Server) deployService(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CommitSha *string `json:"commitSha"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeDecodeError(w, err, "invalid_request")
		return
	}
	if body.CommitSha != nil && gitsrc.ValidRef(*body.CommitSha) != nil {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	svc, ok := s.ownedService(w, r)
	if !ok {
		return
	}
	managed := store.IsManaged(svc.ServiceType)
	sha, event := defaultRef(svc), store.EventBuildRequested
	if managed {
		sha, event = "latest", store.EventDeployRequested
	}
	if body.CommitSha != nil {
		sha = *body.CommitSha
	}

	ctx := r.Context()
	var dep store.Deployment
	err := s.Store.InTx(ctx, func(tx *store.Store) error {
		var err error
		if dep, err = tx.CreateDeployment(ctx, svc.ID, sha); err != nil {
			return err
		}
		_, err = tx.CreateOutboxEvent(ctx, event, map[string]string{"deploymentId": dep.ID})
		return err
	})
	if errors.Is(err, store.ErrNotFound) {
		// Deleted between the ownership check and the insert.
		writeError(w, http.StatusNotFound, "service_not_found")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"deploymentId": dep.ID, "status": dep.Status})
}

// defaultRef is the revision a deploy builds when the caller names none: the
// tip of the service's branch (the builder treats a ref equal to the branch as
// the tip), or HEAD when the branch name cannot double as an image tag (for
// example "feature/x"). The Node API's placeholder "main" is not sent any
// more, so the builder's legacy rule for it can go after cutover.
func defaultRef(svc store.Service) string {
	if gitsrc.ValidRef(svc.Branch) == nil {
		return svc.Branch
	}
	return "HEAD"
}

func (s *Server) getServiceEnv(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.ownedService(w, r)
	if !ok {
		return
	}
	vars, err := s.Store.ListEnvVars(r.Context(), svc.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	reveal := r.URL.Query().Get("reveal") == "true"
	for i := range vars {
		if !reveal {
			vars[i].Value = maskedValue
			continue
		}
		plain, err := s.Secrets.Decrypt(vars[i].Value)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		vars[i].Value = plain
	}
	writeJSON(w, http.StatusOK, vars)
}

func (s *Server) setServiceEnv(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key   string  `json:"key"`
		Value *string `json:"value"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeDecodeError(w, err, "invalid_request")
		return
	}
	if body.Value == nil {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	// The worker writes KEY=VALUE lines to a docker env file, so a value with a
	// line break would not deploy; a NUL byte or invalid UTF-8 would make
	// Postgres fail (500).
	if len(body.Key) > maxEnvKeyLen || !envKeyPattern.MatchString(body.Key) ||
		len(*body.Value) > maxEnvValue || !utf8.ValidString(*body.Value) ||
		strings.ContainsAny(*body.Value, "\r\n\x00") {
		writeError(w, http.StatusBadRequest, "invalid_env_var")
		return
	}
	svc, ok := s.ownedService(w, r)
	if !ok {
		return
	}
	enc, err := s.Secrets.Encrypt(*body.Value)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	ev, err := s.Store.UpsertEnvVar(r.Context(), svc.ID, body.Key, enc)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	ev.Value = maskedValue
	writeJSON(w, http.StatusOK, ev)
}

func (s *Server) deleteServiceEnv(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.ownedService(w, r)
	if !ok {
		return
	}
	// Node-era rows may hold keys the write path now refuses, so the delete is
	// lenient: only what Postgres could not store is answered as "nothing there".
	if key := r.PathValue("key"); key != "" && len(key) <= maxLegacyKey && utf8.ValidString(key) &&
		!strings.ContainsRune(key, 0) {
		if err := s.Store.DeleteEnvVar(r.Context(), svc.ID, key); err != nil {
			s.internalError(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}
