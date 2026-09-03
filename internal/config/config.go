package config

import "flag"

type Config struct {
    Host string
    Port int
}

func Load() Config {
    host := flag.String("host", "0.0.0.0", "bind host")
    port := flag.Int("port", 8080, "listener port")
    flag.Parse()

    return Config{
        Host: *host,
        Port: *port,
    }
}