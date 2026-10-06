package platform

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHealth(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		ready  Readiness
		status int
	}{
		{name: "liveness without dependencies", path: "/health/live", status: http.StatusOK},
		{name: "missing readiness", path: "/health/ready", status: http.StatusServiceUnavailable},
		{name: "failed dependency", path: "/health/ready", ready: func(context.Context) error { return errors.New("private database details") }, status: http.StatusServiceUnavailable},
		{name: "ready", path: "/health/ready", ready: func(ctx context.Context) error {
			if _, ok := ctx.Deadline(); !ok {
				t.Error("readiness needs a deadline")
			}
			return nil
		}, status: http.StatusOK},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			NewHandler(test.ready).ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d", response.Code, test.status)
			}
			if response.Header().Get("Content-Type") != "application/json; charset=utf-8" {
				t.Fatal("missing JSON content type")
			}
			want := "{\"status\":\"ok\"}\n"
			if test.status == http.StatusServiceUnavailable {
				want = "{\"status\":\"not_ready\"}\n"
			}
			if response.Body.String() != want {
				t.Fatalf("unexpected response: %s", response.Body.String())
			}
		})
	}
}

func TestHealthRejectsPost(t *testing.T) {
	response := httptest.NewRecorder()
	NewHandler(nil).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/health/live", nil))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", response.Code)
	}
}

func TestRunStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- Run(ctx, Config{Address: "127.0.0.1:0", ShutdownTimeout: time.Second}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not stop")
	}
}
