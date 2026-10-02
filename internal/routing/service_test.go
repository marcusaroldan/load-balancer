package routing

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/marcusaroldan/load-balancer/internal/pool"
)

func servicePool(t *testing.T, ids ...string) *pool.Pool {
	t.Helper()
	p := &pool.Pool{ID: "svc", Backends: make(map[string]*pool.Backend, len(ids))}
	for _, id := range ids {
		p.Backends[id] = &pool.Backend{ID: id, Address: "http://" + id, Healthy: true}
	}
	return p
}

func TestServiceRoundRobinRotation(t *testing.T) {
	svc := NewService()
	p := servicePool(t, "b1", "b2", "b3")

	seen := []string{}
	for i := 0; i < 6; i++ {
		got, err := svc.Select(StrategyRoundRobin, p, nil)
		if err != nil {
			t.Fatalf("Select: %v", err)
		}
		seen = append(seen, got.ID)
	}
	// Two full cycles over deterministic ID order.
	want := []string{"b1", "b2", "b3", "b1", "b2", "b3"}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("rotation %v, want %v", seen, want)
		}
	}
}

func TestServiceUnknownStrategyErrors(t *testing.T) {
	svc := NewService()
	p := servicePool(t, "b1")
	if _, err := svc.Select("ip_hash", p, nil); !errors.Is(err, ErrUnknownStrategy) {
		t.Fatalf("want ErrUnknownStrategy, got %v", err)
	}
	// Empty name is the documented default, not an error.
	if _, err := svc.Select("", p, nil); err != nil {
		t.Fatalf("empty strategy should default to round robin: %v", err)
	}
}

func TestServiceResetPoolRestartsRotation(t *testing.T) {
	svc := NewService()
	p := servicePool(t, "b1", "b2")

	first, _ := svc.Select(StrategyRoundRobin, p, nil)
	second, _ := svc.Select(StrategyRoundRobin, p, nil)
	if first.ID == second.ID {
		t.Fatal("round robin did not rotate")
	}
	svc.ResetPool(p.ID)
	third, _ := svc.Select(StrategyRoundRobin, p, nil)
	if third.ID != first.ID {
		t.Fatalf("after reset expected %s, got %s", first.ID, third.ID)
	}
}

func TestServiceWeightedCycle(t *testing.T) {
	svc := NewService()
	p := servicePool(t, "heavy", "light")
	policy := map[string]BackendPolicy{
		"heavy": {Weight: 3},
		"light": {Weight: 1},
	}

	counts := map[string]int{}
	for i := 0; i < 8; i++ {
		got, err := svc.Select(StrategyWeighted, p, policy)
		if err != nil {
			t.Fatalf("Select: %v", err)
		}
		counts[got.ID]++
	}
	if counts["heavy"] != 6 || counts["light"] != 2 {
		t.Fatalf("weighted distribution %v, want heavy=6 light=2", counts)
	}
}

func TestServiceLeastConnectionsTieBreakDeterministic(t *testing.T) {
	svc := NewService()
	p := servicePool(t, "a", "b", "c")
	policy := map[string]BackendPolicy{
		"a": {ActiveConnections: 5},
		"b": {ActiveConnections: 1},
		"c": {ActiveConnections: 1},
	}
	for i := 0; i < 3; i++ {
		got, err := svc.Select(StrategyLeastConnections, p, policy)
		if err != nil {
			t.Fatalf("Select: %v", err)
		}
		if got.ID != "b" {
			t.Fatalf("tie must break by lowest ID, got %s", got.ID)
		}
	}
}

func TestServiceExcludesUnhealthy(t *testing.T) {
	svc := NewService()
	p := servicePool(t, "b1", "b2")
	p.Backends["b1"].Healthy = false

	for i := 0; i < 4; i++ {
		got, err := svc.Select(StrategyRoundRobin, p, nil)
		if err != nil {
			t.Fatalf("Select: %v", err)
		}
		if got.ID != "b2" {
			t.Fatalf("unhealthy backend selected: %s", got.ID)
		}
	}

	p.Backends["b2"].Healthy = false
	if _, err := svc.Select(StrategyRoundRobin, p, nil); !errors.Is(err, ErrNoHealthyBackends) {
		t.Fatalf("want ErrNoHealthyBackends, got %v", err)
	}
}

func TestServiceConcurrentSelectAndReset(t *testing.T) {
	svc := NewService()
	p := servicePool(t, "b1", "b2", "b3")

	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(2)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				if _, err := svc.Select(StrategyRoundRobin, p, nil); err != nil {
					t.Errorf("concurrent select: %v", err)
					return
				}
			}
		}
	}()
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				svc.ResetPool(p.ID)
			}
		}
	}()
	time.Sleep(20 * time.Millisecond)
	close(stop)
	wg.Wait()
}
