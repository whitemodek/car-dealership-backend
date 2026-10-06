package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/whitemodek/car-dealership-backend/backend/internal/platform"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	output := flag.String("output", ".env", "new local environment file (never overwritten)")
	address := flag.String("db-host", "db:5432", "database host:port")
	database := flag.String("db-name", "dealership", "database name")
	ci := flag.Bool("ci", false, "append ephemeral credentials to GitHub Actions GITHUB_ENV")
	flag.Parse()
	env, err := platform.NewEnvironment(*address, *database)
	if err != nil {
		return err
	}
	if *ci {
		path := os.Getenv("GITHUB_ENV")
		if os.Getenv("GITHUB_ACTIONS") != "true" || path == "" {
			return fmt.Errorf("-ci requires a GitHub Actions environment")
		}
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return fmt.Errorf("cannot open GITHUB_ENV")
		}
		defer file.Close()
		// GitHub masks this value before later steps can log connection strings.
		fmt.Println("::add-mask::" + env.Password)
		if _, err = file.WriteString(env.CIFile()); err != nil {
			return fmt.Errorf("cannot write GITHUB_ENV")
		}
		return file.Close()
	}
	if err := createLocalFile(*output, env.LocalFile()); err != nil {
		return err
	}
	fmt.Println("Local environment file created. Credentials were not printed.")
	return nil
}

func createLocalFile(path, content string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("cannot create environment file; existing files are never overwritten")
	}
	defer file.Close()
	if _, err = file.WriteString(content); err != nil {
		return fmt.Errorf("cannot write environment file")
	}
	return file.Close()
}
