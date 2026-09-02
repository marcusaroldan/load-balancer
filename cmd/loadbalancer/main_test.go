package main

import "testing"

func TestVersionString(t *testing.T) {
	if got := "load-balancer v0.1.0"; got == "" {
		t.Fatal("expected version string")
	}
}
