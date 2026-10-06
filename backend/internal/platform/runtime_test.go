package platform

import "testing"

func TestRuntimeConfiguration(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		invalid bool
	}{
		{"missing database", map[string]string{}, true},
		{"production insecure database", map[string]string{"DATABASE_URL": "postgres://localhost/dealer?sslmode=disable", "APP_ENV": "production"}, true},
		{"production", map[string]string{"DATABASE_URL": "postgres://localhost/dealer?sslmode=verify-full", "APP_ENV": "production", "CORS_ORIGINS": "https://dealer.example"}, false},
		{"wildcard origin", map[string]string{"DATABASE_URL": "postgres://localhost/dealer", "CORS_ORIGINS": "*"}, true},
		{"trust all proxies", map[string]string{"DATABASE_URL": "postgres://localhost/dealer", "TRUSTED_PROXY_CIDRS": "0.0.0.0/0"}, true},
		{"trusted proxy subnet", map[string]string{"DATABASE_URL": "postgres://localhost/dealer", "TRUSTED_PROXY_CIDRS": "10.20.0.0/24"}, false},
		{"insecure production origin", map[string]string{"DATABASE_URL": "postgres://localhost/dealer?sslmode=verify-full", "APP_ENV": "production", "CORS_ORIGINS": "http://dealer.example"}, true},
		{"too short reservation", map[string]string{"DATABASE_URL": "postgres://localhost/dealer", "RESERVATION_DURATION": "1s"}, true},
		{"unsigned webhook", map[string]string{"DATABASE_URL": "postgres://localhost/dealer", "WEBHOOK_URL": "https://hooks.example"}, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := loadConfig(func(key string) (string, bool) { v, ok := test.env[key]; return v, ok })
			if (err != nil) != test.invalid {
				t.Fatalf("unexpected configuration error: %v", err)
			}
		})
	}
}
