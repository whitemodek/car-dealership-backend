package platform

import (
	"testing"
	"time"
)

func TestConfig(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		invalid bool
	}{
		{name: "defaults"},
		{name: "overrides", env: map[string]string{"HTTP_ADDR": "127.0.0.1:9000", "SHUTDOWN_TIMEOUT": "20s"}},
		{name: "empty address", env: map[string]string{"HTTP_ADDR": ""}, invalid: true},
		{name: "invalid port", env: map[string]string{"HTTP_ADDR": ":70000"}, invalid: true},
		{name: "missing port", env: map[string]string{"HTTP_ADDR": "localhost"}, invalid: true},
		{name: "zero timeout", env: map[string]string{"SHUTDOWN_TIMEOUT": "0s"}, invalid: true},
		{name: "invalid timeout", env: map[string]string{"SHUTDOWN_TIMEOUT": "invalid"}, invalid: true},
		{name: "excessive timeout", env: map[string]string{"SHUTDOWN_TIMEOUT": "2m"}, invalid: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config, err := loadConfig(func(key string) (string, bool) { value, ok := test.env[key]; return value, ok })
			if (err != nil) != test.invalid {
				t.Fatalf("unexpected error: %v", err)
			}
			if test.name == "defaults" && (config.Address != ":8080" || config.ShutdownTimeout != 10*time.Second) {
				t.Fatalf("unexpected defaults: %+v", config)
			}
			if test.name == "overrides" && (config.Address != "127.0.0.1:9000" || config.ShutdownTimeout != 20*time.Second) {
				t.Fatalf("overrides not applied: %+v", config)
			}
		})
	}
}
