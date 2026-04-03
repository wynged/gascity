package tmux

import (
	"testing"
)

func TestSortedEnvKeys(t *testing.T) {
	env := map[string]string{
		"Z_VAR":   "z",
		"A_VAR":   "a",
		"UNSET_B": "",
		"M_VAR":   "m",
		"UNSET_A": "",
	}
	all, unset, set := sortedEnvKeys(env)

	// All keys sorted.
	wantAll := []string{"A_VAR", "M_VAR", "UNSET_A", "UNSET_B", "Z_VAR"}
	if len(all) != len(wantAll) {
		t.Fatalf("allKeys: got %v, want %v", all, wantAll)
	}
	for i, k := range wantAll {
		if all[i] != k {
			t.Errorf("allKeys[%d] = %q, want %q", i, all[i], k)
		}
	}

	// Unset keys sorted.
	wantUnset := []string{"UNSET_A", "UNSET_B"}
	if len(unset) != len(wantUnset) {
		t.Fatalf("unsetKeys: got %v, want %v", unset, wantUnset)
	}
	for i, k := range wantUnset {
		if unset[i] != k {
			t.Errorf("unsetKeys[%d] = %q, want %q", i, unset[i], k)
		}
	}

	// Set keys sorted.
	wantSet := []string{"A_VAR", "M_VAR", "Z_VAR"}
	if len(set) != len(wantSet) {
		t.Fatalf("setKeys: got %v, want %v", set, wantSet)
	}
	for i, k := range wantSet {
		if set[i] != k {
			t.Errorf("setKeys[%d] = %q, want %q", i, set[i], k)
		}
	}
}

func TestSortedEnvKeys_Empty(t *testing.T) {
	all, unset, set := sortedEnvKeys(nil)
	if len(all) != 0 || len(unset) != 0 || len(set) != 0 {
		t.Errorf("expected all empty, got all=%v unset=%v set=%v", all, unset, set)
	}
}

func TestWrapCommandWithUnsetEnv(t *testing.T) {
	tests := []struct {
		name    string
		command string
		unset   []string
		want    string
	}{
		{"no unset keys", "claude", nil, "claude"},
		{"empty command", "", []string{"FOO"}, ""},
		{"single unset", "claude --flag", []string{"STALE"}, "env -u STALE claude --flag"},
		{"multiple unset", "claude", []string{"A", "B"}, "env -u A -u B claude"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wrapCommandWithUnsetEnv(tt.command, tt.unset)
			if got != tt.want {
				t.Errorf("wrapCommandWithUnsetEnv(%q, %v) = %q, want %q", tt.command, tt.unset, got, tt.want)
			}
		})
	}
}

func TestWrapCommandWithEnv(t *testing.T) {
	tests := []struct {
		name    string
		command string
		env     map[string]string
		want    string
	}{
		{
			"empty env",
			"claude --session-id abc",
			nil,
			"claude --session-id abc",
		},
		{
			"empty command",
			"",
			map[string]string{"GC_CITY": "/tmp"},
			"",
		},
		{
			"set only",
			"claude",
			map[string]string{"GC_CITY": "/path", "GC_AGENT": "dev"},
			"env GC_AGENT=dev GC_CITY=/path exec claude",
		},
		{
			"unset only",
			"claude",
			map[string]string{"STALE": ""},
			"env -u STALE exec claude",
		},
		{
			"mixed set and unset",
			"claude --flag",
			map[string]string{"GC_CITY": "/path", "STALE": "", "GC_AGENT": "dev"},
			"env -u STALE GC_AGENT=dev GC_CITY=/path exec claude --flag",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := WrapCommandWithEnv(tt.command, tt.env)
			if got != tt.want {
				t.Errorf("WrapCommandWithEnv(%q, %v) = %q, want %q", tt.command, tt.env, got, tt.want)
			}
		})
	}
}
