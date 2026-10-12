package registry

import (
	"errors"
	"testing"

	"github.com/marcusaroldan/load-balancer/internal/config"
)

// TestCreatePoolHostAdmission covers multi-pool host routing plumbing:
// hosts normalize on admission, duplicates are rejected against existing
// pools, and the snapshot exposes the claim through pool metadata.
func TestCreatePoolHostAdmission(t *testing.T) {
	r := New()
	if err := r.CreatePool(config.PoolConfig{ID: "api", Host: " API.Example.COM "}); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Duplicate claim (case-insensitive) is rejected.
	err := r.CreatePool(config.PoolConfig{ID: "other", Host: "api.example.com"})
	if !errors.Is(err, config.ErrDuplicateHost) {
		t.Fatalf("duplicate host: got %v, want ErrDuplicateHost", err)
	}

	// A hostless pool coexists fine.
	if err := r.CreatePool(config.PoolConfig{ID: "default"}); err != nil {
		t.Fatalf("create hostless pool: %v", err)
	}

	// Snapshot metadata exposes the normalized host claim.
	snap := r.Snapshot()
	p, ok := snap.Pool("api")
	if !ok {
		t.Fatal("api pool missing from snapshot")
	}
	if got := p.Metadata["host"]; got != "api.example.com" {
		t.Fatalf("snapshot host metadata: got %q", got)
	}
	if p, ok := snap.Pool("default"); ok && p.Metadata["host"] != "" {
		t.Fatalf("hostless pool leaked host metadata: %q", p.Metadata["host"])
	}

	// Deleting the claiming pool frees the host.
	if err := r.DeletePool("api"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := r.CreatePool(config.PoolConfig{ID: "api2", Host: "api.example.com"}); err != nil {
		t.Fatalf("reclaim after delete: %v", err)
	}
}
