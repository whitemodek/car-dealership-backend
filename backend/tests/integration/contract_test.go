package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/whitemodek/car-dealership-backend/backend/internal/domain"
)

func contractHandler(t *testing.T, handler http.Handler) http.Handler {
	t.Helper()
	doc, err := openapi3.NewLoader().LoadFromFile("../../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		input, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		r.Body = io.NopCloser(bytes.NewReader(input))
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, r)
		if r.Method != http.MethodOptions {
			segments := strings.Split(r.URL.Path, "/")
			for i, part := range segments {
				if domain.ValidID(part) {
					segments[i] = "{id}"
				}
			}
			path := doc.Paths.Find(strings.Join(segments, "/"))
			if path == nil {
				t.Fatalf("endpoint missing from OpenAPI: %s", r.URL.Path)
			}
			operation := path.GetOperation(r.Method)
			if operation == nil {
				t.Fatalf("method missing from OpenAPI: %s %s", r.Method, r.URL.Path)
			}
			response := operation.Responses.Status(result.Code)
			if response == nil {
				response = operation.Responses.Default()
			}
			if response == nil {
				t.Fatalf("undocumented status: %d", result.Code)
			}
			if result.Code != 204 {
				media := response.Value.Content["application/json"]
				if media == nil || media.Schema == nil {
					t.Fatalf("missing response schema for %s", r.URL.Path)
				}
				var value any
				if err = json.Unmarshal(result.Body.Bytes(), &value); err != nil {
					t.Fatal(err)
				}
				if err = media.Schema.Value.VisitJSON(value); err != nil {
					t.Errorf("response violates OpenAPI for %s %s: %v", r.Method, r.URL.Path, err)
				}
			}
			if result.Code >= 200 && result.Code < 300 && operation.RequestBody != nil {
				var value any
				if err = json.Unmarshal(input, &value); err != nil {
					t.Fatal(err)
				}
				if err = operation.RequestBody.Value.Content["application/json"].Schema.Value.VisitJSON(value); err != nil {
					t.Errorf("accepted request violates OpenAPI: %v", err)
				}
			}
		}
		for key, values := range result.Header() {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(result.Code)
		_, _ = w.Write(result.Body.Bytes())
	})
}
