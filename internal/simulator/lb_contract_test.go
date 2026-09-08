package simulator

import (
	"reflect"
	"testing"
)

func TestLoadBalancerAdapterContractAllowsOnlyExternalControls(t *testing.T) {
	adapterType := reflect.TypeOf((*LoadBalancerAdapter)(nil)).Elem()
	allowed := map[string]bool{
		"Start":                 true,
		"Stop":                  true,
		"Endpoint":              true,
		"ConfigureScenario":     true,
		"SetStrategy":           true,
		"SetBackendPool":        true,
		"SetBackendWeight":      true,
		"SetBackendHealth":      true,
		"SetBackendLatency":     true,
		"SetBackendFailureRate": true,
		"RemoveBackend":         true,
		"Snapshot":              true,
	}

	for i := 0; i < adapterType.NumMethod(); i++ {
		method := adapterType.Method(i)
		if !allowed[method.Name] {
			t.Fatalf("unexpected adapter method %q in public contract", method.Name)
		}
	}
}

func TestLoadBalancerSnapshotDoesNotExposeInternalRoutingState(t *testing.T) {
	typeOf := reflect.TypeOf(LoadBalancerSnapshot{})
	forbidden := []string{
		"SelectedBackend",
		"SelectedTarget",
		"RouteHistory",
		"RoutingState",
		"InternalDecision",
	}

	for _, fieldName := range forbidden {
		if _, ok := typeOf.FieldByName(fieldName); ok {
			t.Fatalf("load balancer snapshot must not expose internal field %q", fieldName)
		}
	}
}
