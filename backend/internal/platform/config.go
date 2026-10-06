package platform

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"time"
)

type Config struct {
	Address         string
	ShutdownTimeout time.Duration
}

func LoadConfig() (Config, error) {
	return loadConfig(os.LookupEnv)
}

func loadConfig(lookup func(string) (string, bool)) (Config, error) {
	config := Config{Address: ":8080", ShutdownTimeout: 10 * time.Second}
	if value, ok := lookup("HTTP_ADDR"); ok {
		config.Address = value
	}
	_, port, err := net.SplitHostPort(config.Address)
	if err != nil {
		return Config{}, fmt.Errorf("HTTP_ADDR must be host:port: %w", err)
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return Config{}, fmt.Errorf("HTTP_ADDR port must be between 1 and 65535")
	}
	if value, ok := lookup("SHUTDOWN_TIMEOUT"); ok {
		config.ShutdownTimeout, err = time.ParseDuration(value)
		if err != nil || config.ShutdownTimeout <= 0 || config.ShutdownTimeout > time.Minute {
			return Config{}, fmt.Errorf("SHUTDOWN_TIMEOUT must be positive and at most 1m")
		}
	}
	return config, nil
}
