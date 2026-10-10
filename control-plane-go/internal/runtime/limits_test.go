package runtime

import "testing"

func TestNewLimitsRejectsUnlimitedOrEmpty(t *testing.T) {
	l, err := NewLimits("512m", "0.5", 256)
	if err != nil || l != (Limits{Memory: "512m", CPUs: "0.5", Pids: 256}) {
		t.Fatalf("valid: %+v %v", l, err)
	}
	for name, c := range map[string]struct {
		mem, cpus string
		pids      int
	}{
		"pids zero (docker: unlimited)":     {"512m", "0.5", 0},
		"pids negative (docker: unlimited)": {"512m", "0.5", -1},
		"empty memory":                      {"", "0.5", 256},
		"blank memory":                      {"  ", "0.5", 256},
		"empty cpus":                        {"512m", "", 256},
	} {
		if _, err := NewLimits(c.mem, c.cpus, c.pids); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
