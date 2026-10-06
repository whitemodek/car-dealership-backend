package api

import (
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestClientIPTrustBoundary(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	tests := []struct {
		name, peer, header, want string
		invalid                  bool
	}{
		{"untrusted spoof", "192.0.2.1:1234", "198.51.100.8", "192.0.2.1", false},
		{"trusted edge", "10.0.0.1:1234", "198.51.100.8", "198.51.100.8", false},
		{"untrusted intermediate", "10.0.0.1:1234", "203.0.113.9, 192.0.2.1", "192.0.2.1", false},
		{"multiple trusted hops", "10.0.0.1:1234", "198.51.100.8, 10.0.0.2", "198.51.100.8", false},
		{"invalid trusted header", "10.0.0.1:1234", "bad", "", true},
		{"invalid untrusted header ignored", "192.0.2.1:1234", "bad", "192.0.2.1", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = test.peer
			r.Header.Set("X-Forwarded-For", test.header)
			got, err := clientIP(r, trusted)
			if (err != nil) != test.invalid || got != test.want {
				t.Fatalf("got %s / %v", got, err)
			}
		})
	}
}
