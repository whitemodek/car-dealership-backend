package platform

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestRepositoryExamplesDoNotEmbedDatabasePasswords(t *testing.T) {
	credentialURL := regexp.MustCompile(`postgres(?:ql)?://[^\s:/]+:[^@\s]+@`)
	files := []string{"../../../.github/workflows/ci.yml", "../../../.env.example", "../../../README.md", "../../../docs/PERFORMANCE.md"}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if credentialURL.Match(data) {
			t.Errorf("%s contains an embedded database password; construct URLs from runtime credentials", file)
		}
		if strings.HasSuffix(file, ".env.example") {
			for _, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(line, "POSTGRES_PASSWORD=") && strings.TrimSpace(strings.TrimPrefix(line, "POSTGRES_PASSWORD=")) != "" {
					t.Errorf("%s must leave POSTGRES_PASSWORD empty", file)
				}
			}
		}
	}
}
