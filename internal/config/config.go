package config

import (
	"flag"
	"os"
)

const (
	defaultHost = "0.0.0.0"
	defaultPort = 8080
)

type Config struct {
	Host string
	Port int
}

func Load() Config {
	fs := flag.NewFlagSet("load-balancer", flag.ContinueOnError)
	return loadFromFlagSet(fs, os.Args[1:])
}

func loadFromFlagSet(fs *flag.FlagSet, args []string) Config {
	if fs == nil {
		fs = flag.NewFlagSet("load-balancer", flag.ContinueOnError)
	}

	host := fs.String("host", defaultHost, "bind host")
	port := fs.Int("port", defaultPort, "listener port")
	if err := fs.Parse(args); err != nil {
		return Config{Host: defaultHost, Port: defaultPort}
	}

	return Config{Host: *host, Port: *port}
}
