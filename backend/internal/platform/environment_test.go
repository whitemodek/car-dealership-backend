package platform

import (
	"net/url"
	"strings"
	"testing"
)

func TestEnvironmentGeneratesFreshMatchingCredentials(t *testing.T) {
	first, err := NewEnvironment("db:5432", "dealership")
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewEnvironment("db:5432", "dealership")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Password) != 64 || first.Password == second.Password {
		t.Fatal("password must be a fresh 256-bit random value")
	}
	for _, entry := range []struct{ connection, path string }{{first.DatabaseURL, "/dealership"}, {first.RestoreDatabaseURL, "/dealership_restore_check"}} {
		u, err := url.Parse(entry.connection)
		if err != nil {
			t.Fatal("generated URL is invalid")
		}
		password, ok := u.User.Password()
		if !ok || password != first.Password || u.User.Username() != "dealership" || u.Host != "db:5432" || u.Path != entry.path {
			t.Fatal("generated URL does not match database credentials")
		}
	}
	if !strings.Contains(first.LocalFile(), "POSTGRES_PASSWORD="+first.Password+"\n") {
		t.Fatal("local file is missing generated credentials")
	}
	if !strings.Contains(first.CIFile(), "RESTORE_DATABASE_URL="+first.RestoreDatabaseURL+"\n") {
		t.Fatal("CI environment is missing restore connection")
	}
}

func TestEnvironmentRejectsInjectionAndInvalidAddresses(t *testing.T) {
	for _, input := range []struct{ host, database string }{{"db:0", "dealership"}, {"db:65536", "dealership"}, {"db\nEXTRA=value:5432", "dealership"}, {"db:5432", "dealership\nEXTRA=value"}, {"db:5432", "../escape"}} {
		if _, err := NewEnvironment(input.host, input.database); err == nil {
			t.Fatal("invalid environment input was accepted")
		}
	}
}
