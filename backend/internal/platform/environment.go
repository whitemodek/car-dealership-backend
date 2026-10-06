package platform

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
)

type Environment struct {
	Password           string
	DatabaseURL        string
	RestoreDatabaseURL string
}

func NewEnvironment(address, database string) (Environment, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return Environment{}, fmt.Errorf("database address must be host:port")
	}
	if _, err := netip.ParseAddr(host); err != nil && !regexp.MustCompile(`^[a-zA-Z0-9.-]+$`).MatchString(host) {
		return Environment{}, fmt.Errorf("invalid database host")
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return Environment{}, fmt.Errorf("invalid database port")
	}
	if !regexp.MustCompile(`^[a-z][a-z0-9_]{0,48}$`).MatchString(database) {
		return Environment{}, fmt.Errorf("invalid database name")
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return Environment{}, err
	}
	password := hex.EncodeToString(random[:])
	u := url.URL{Scheme: "postgres", Host: address, User: url.UserPassword("dealership", password), Path: "/" + database, RawQuery: "sslmode=disable"}
	env := Environment{Password: password, DatabaseURL: u.String()}
	u.Path = "/" + database + "_restore_check"
	env.RestoreDatabaseURL = u.String()
	return env, nil
}

func (e Environment) LocalFile() string {
	return "# Generated local-development credentials. Do not commit this file.\n" +
		"POSTGRES_PASSWORD=" + e.Password + "\nDATABASE_URL=" + e.DatabaseURL +
		"\nCORS_ORIGINS=http://localhost:3000\nRESERVATION_DURATION=24h\nWEBHOOK_URL=\nWEBHOOK_SECRET=\n"
}

func (e Environment) CIFile() string {
	return "POSTGRES_PASSWORD=" + e.Password + "\nDATABASE_URL=" + e.DatabaseURL +
		"\nTEST_DATABASE_URL=" + e.DatabaseURL + "\nRESTORE_DATABASE_URL=" + e.RestoreDatabaseURL + "\n"
}
