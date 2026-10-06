package platform

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Address             string
	ShutdownTimeout     time.Duration
	DatabaseURL         string
	Environment         string
	Origins             []string
	ReservationDuration time.Duration
	WebhookURL          string
	WebhookSecret       string
	TrustedProxies      []netip.Prefix
}

func LoadConfig() (Config, error) {
	return loadConfig(os.LookupEnv)
}

func loadConfig(lookup func(string) (string, bool)) (Config, error) {
	config := Config{Address: ":8080", ShutdownTimeout: 10 * time.Second, Environment: "development", ReservationDuration: 24 * time.Hour}
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
	config.DatabaseURL, _ = lookup("DATABASE_URL")
	db, err := url.Parse(config.DatabaseURL)
	if err != nil || (db.Scheme != "postgres" && db.Scheme != "postgresql") || db.Host == "" || db.Path == "" {
		return Config{}, fmt.Errorf("DATABASE_URL must be a PostgreSQL URL")
	}
	if value, ok := lookup("APP_ENV"); ok {
		config.Environment = value
	}
	if config.Environment != "development" && config.Environment != "production" && config.Environment != "test" {
		return Config{}, fmt.Errorf("APP_ENV must be development, test or production")
	}
	if config.Environment == "production" && db.Query().Get("sslmode") != "verify-full" {
		return Config{}, fmt.Errorf("production DATABASE_URL requires sslmode=verify-full")
	}
	if value, ok := lookup("CORS_ORIGINS"); ok && value != "" {
		for _, origin := range strings.Split(value, ",") {
			origin = strings.TrimSpace(origin)
			u, err := url.Parse(origin)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil || (config.Environment == "production" && u.Scheme != "https") {
				return Config{}, fmt.Errorf("invalid CORS_ORIGINS")
			}
			config.Origins = append(config.Origins, origin)
		}
	}
	if value, ok := lookup("RESERVATION_DURATION"); ok {
		config.ReservationDuration, err = time.ParseDuration(value)
		if err != nil || config.ReservationDuration < 15*time.Minute || config.ReservationDuration > 7*24*time.Hour {
			return Config{}, fmt.Errorf("RESERVATION_DURATION must be between 15m and 168h")
		}
	}
	config.WebhookURL, _ = lookup("WEBHOOK_URL")
	if value, ok := lookup("TRUSTED_PROXY_CIDRS"); ok && value != "" {
		for _, cidr := range strings.Split(value, ",") {
			prefix, err := netip.ParsePrefix(strings.TrimSpace(cidr))
			if err != nil {
				return Config{}, fmt.Errorf("TRUSTED_PROXY_CIDRS must contain valid CIDRs")
			}
			if prefix.Bits() == 0 {
				return Config{}, fmt.Errorf("TRUSTED_PROXY_CIDRS must not trust the entire internet")
			}
			config.TrustedProxies = append(config.TrustedProxies, prefix.Masked())
		}
	}
	config.WebhookSecret, _ = lookup("WEBHOOK_SECRET")
	if config.WebhookURL != "" {
		u, err := url.Parse(config.WebhookURL)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || len(config.WebhookSecret) < 32 {
			return Config{}, fmt.Errorf("WEBHOOK_URL requires HTTPS and WEBHOOK_SECRET of at least 32 bytes")
		}
	} else if config.WebhookSecret != "" {
		return Config{}, fmt.Errorf("WEBHOOK_SECRET requires WEBHOOK_URL")
	}
	return config, nil
}
