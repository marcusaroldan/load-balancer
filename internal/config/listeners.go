// Package config holds the load balancer's configuration types and rules.
// listeners.go defines the CLI flag layer; runtime.go defines the canonical
// desired-state model, defaulting, and validation (PROD-001).
package config

import (
	"flag"
	"os"
)

const (
	defaultHost = "0.0.0.0"
	defaultPort = 8080
)

// ListenerFlags is the minimal CLI surface parsed by cmd/loadbalancer today.
// It is superseded by Config in runtime.go once PROD-008 wires the standalone
// runtime; kept separate so both can coexist during migration.
type ListenerFlags struct {
	Host string
	Port int
}

func Load() ListenerFlags {
	fs := flag.NewFlagSet("load-balancer", flag.ContinueOnError)
	return loadFromFlagSet(fs, os.Args[1:])
}

func loadFromFlagSet(fs *flag.FlagSet, args []string) ListenerFlags {
	if fs == nil {
		fs = flag.NewFlagSet("load-balancer", flag.ContinueOnError)
	}

	host := fs.String("host", defaultHost, "bind host")
	port := fs.Int("port", defaultPort, "listener port")
	if err := fs.Parse(args); err != nil {
		return ListenerFlags{Host: defaultHost, Port: defaultPort}
	}

	return ListenerFlags{Host: *host, Port: *port}
}
