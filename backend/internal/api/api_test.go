package api

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeRejectsAmbiguousRequests(t *testing.T) {
	for _, body := range []string{`{"name":"one","unknown":true}`, `{"name":"one"}{"name":"two"}`, `null`, `["one"]`, strings.Repeat("x", 10)} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		var in struct {
			Name string `json:"name"`
		}
		if err := decode(r, &in); err == nil {
			t.Fatalf("accepted invalid JSON: %s", body)
		}
	}
}
func TestPagination(t *testing.T) {
	for _, query := range []string{"limit=0", "limit=101", "offset=-1", "offset=100001", "limit=bad"} {
		if _, _, err := page(httptest.NewRequest("GET", "/?"+query, nil)); err == nil {
			t.Fatalf("accepted %s", query)
		}
	}
	limit, offset, err := page(httptest.NewRequest("GET", "/", nil))
	if err != nil || limit != 20 || offset != 0 {
		t.Fatal("incorrect defaults")
	}
}
