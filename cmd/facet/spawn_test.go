package main

import (
	"testing"

	"github.com/RiccardoCereghino/facet/internal/routing"
)

// resolveLaunch governs the path with NO pane: claude would take over the
// terminal `facet spawn` was run from. That is why it stays off unless routing
// or a named flag turns it on, even though the same --claude flag defaults to
// true for a pane.
func TestResolveLaunch(t *testing.T) {
	on := &routing.Routing{Spawn: &routing.Spawn{RC: true}}
	off := &routing.Routing{}

	tests := []struct {
		name  string
		argv  []string
		route *routing.Routing
		want  bool
	}{
		{"no flags, routing silent: launch nothing", nil, off, false},
		{"no flags, no routing at all: launch nothing", nil, nil, false},
		{"no flags, routing says rc: launch", nil, on, true},

		{"--claude, routing silent: launch", []string{"--claude=true"}, off, true},
		{"--rc, routing silent: launch", []string{"--rc"}, off, true},

		{"--claude=false beats routing", []string{"--claude=false"}, on, false},
		{"--no-rc beats routing", []string{"--no-rc"}, on, false},
		{"--no-rc beats --rc and routing", []string{"--rc", "--no-rc"}, on, false},

		// --remote only says HOW to launch, never WHETHER.
		{"--remote=false alone launches nothing", []string{"--remote=false"}, off, false},
		{"--remote=false still launches when routing says rc", []string{"--remote=false"}, on, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := spawnOpts{Agent: resolveFlags(t, tt.argv...)}
			if got := resolveLaunch(o, tt.route); got != tt.want {
				t.Errorf("resolveLaunch(%v, rc=%v) = %v, want %v",
					tt.argv, tt.route.SpawnRC(), got, tt.want)
			}
		})
	}
}
