package selector

import (
	"testing"

	"github.com/marcusaroldan/load-balancer/internal/pool"
)

func TestRoundRobinRotatesAmongHealthyBackends(t *testing.T) {
	selector := NewRoundRobin()

	p := &pool.Pool{
		ID: "pool-a",
		Backends: map[string]*pool.Backend{
			"b1": {ID: "b1", Healthy: true},
			"b2": {ID: "b2", Healthy: true},
			"b3": {ID: "b3", Healthy: true},
		},
	}

	sequence := make([]string, 0, 6)
	for i := 0; i < 6; i++ {
		backend, err := selector.Next(p)
		if err != nil {
			t.Fatalf("Next returned err: %v", err)
		}
		sequence = append(sequence, backend.ID)
	}

	want := []string{"b1", "b2", "b3", "b1", "b2", "b3"}
	for i := range want {
		if sequence[i] != want[i] {
			t.Fatalf("sequence mismatch at %d: got %q want %q", i, sequence, want)
		}
	}
}

func TestRoundRobinSkipsUnhealthyBackends(t *testing.T) {
	selector := NewRoundRobin()

	p := &pool.Pool{
		ID: "pool-a",
		Backends: map[string]*pool.Backend{
			"b1": {ID: "b1", Healthy: true},
			"b2": {ID: "b2", Healthy: false},
			"b3": {ID: "b3", Healthy: true},
		},
	}

	for i := 0; i < 6; i++ {
		backend, err := selector.Next(p)
		if err != nil {
			t.Fatalf("Next returned err: %v", err)
		}
		if backend.ID == "b2" {
			t.Fatal("unexpected unhealthy backend selected")
		}
	}
}

func TestRoundRobinReturnsErrorWhenNoHealthyBackendsExist(t *testing.T) {
	selector := NewRoundRobin()

	p := &pool.Pool{
		ID: "pool-a",
		Backends: map[string]*pool.Backend{
			"b1": {ID: "b1", Healthy: false},
			"b2": {ID: "b2", Healthy: false},
		},
	}

	if _, err := selector.Next(p); err == nil {
		t.Fatal("expected error when no healthy backends exist")
	}
}

func TestRoundRobinTracksStatePerPool(t *testing.T) {
	selector := NewRoundRobin()

	poolA := &pool.Pool{ID: "pool-a", Backends: map[string]*pool.Backend{
		"b1": {ID: "b1", Healthy: true},
		"b2": {ID: "b2", Healthy: true},
	}}
	poolB := &pool.Pool{ID: "pool-b", Backends: map[string]*pool.Backend{
		"c1": {ID: "c1", Healthy: true},
		"c2": {ID: "c2", Healthy: true},
	}}

	backendA1, _ := selector.Next(poolA)
	backendB1, _ := selector.Next(poolB)
	backendA2, _ := selector.Next(poolA)

	if backendA1.ID == backendA2.ID && backendB1.ID != "c1" {
		t.Fatal("pool-specific round-robin state is not isolated")
	}
	if backendA1.ID == "c1" || backendB1.ID == "b1" {
		t.Fatal("selector is incorrectly reusing cross-pool state")
	}
}

func TestSetHealthUpdatesSelectorState(t *testing.T) {
	selector := NewRoundRobin()

	p := &pool.Pool{ID: "pool-a", Backends: map[string]*pool.Backend{
		"b1": {ID: "b1", Healthy: true},
		"b2": {ID: "b2", Healthy: true},
	}}

	if err := selector.SetHealth(p, "b1", false); err != nil {
		t.Fatalf("SetHealth returned err: %v", err)
	}

	backend, err := selector.Next(p)
	if err != nil {
		t.Fatalf("Next returned err after health update: %v", err)
	}
	if backend.ID != "b2" {
		t.Fatalf("expected first healthy backend to be b2, got %q", backend.ID)
	}
}
