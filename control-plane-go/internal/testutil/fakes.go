package testutil

import (
	"context"
	"strings"
	"sync"
)

// Call is one command recorded by FakeRunner.
type Call struct {
	Dir  string
	Name string
	Args []string
}

func (c Call) String() string { return strings.TrimSpace(c.Name + " " + strings.Join(c.Args, " ")) }

// FakeRunner records commands instead of executing them.
type FakeRunner struct {
	mu    sync.Mutex
	calls []Call
	// Handle, if set, decides each call's stdout and error. Run may be called
	// from several goroutines, so Handle may be called concurrently and must
	// be safe for that.
	Handle func(c Call) (string, error)
}

func (f *FakeRunner) Run(_ context.Context, dir, name string, args ...string) (string, error) {
	c := Call{Dir: dir, Name: name, Args: append([]string(nil), args...)}
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()
	if f.Handle != nil {
		return f.Handle(c)
	}
	return "", nil
}

// Commands returns every recorded call as "name arg1 arg2".
func (f *FakeRunner) Commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	for i, c := range f.calls {
		out[i] = c.String()
	}
	return out
}
