package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/whitemodek/car-dealership-backend/backend/internal/database"
	"github.com/whitemodek/car-dealership-backend/backend/internal/identity"
	"github.com/whitemodek/car-dealership-backend/backend/internal/platform"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) != 2 {
		return fmt.Errorf("usage: manage migrate | create-admin | seed-demo")
	}
	if os.Args[1] != "migrate" && os.Args[1] != "create-admin" && os.Args[1] != "seed-demo" {
		return fmt.Errorf("unknown command")
	}
	config, err := platform.LoadConfig()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := database.Open(ctx, config.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if os.Args[1] == "seed-demo" {
		if config.Environment != "development" {
			return fmt.Errorf("demo seed is only allowed in development")
		}
		if err = database.SeedDemo(ctx, pool); err != nil {
			return err
		}
		fmt.Println("demo catalog created")
		return nil
	}
	if os.Args[1] == "migrate" {
		if err = database.Migrate(ctx, pool); err != nil {
			return err
		}
		fmt.Println("migrations applied")
		return nil
	}
	store, err := identity.New(pool)
	if err != nil {
		return err
	}
	staff, err := store.Create(ctx, "", os.Getenv("ADMIN_EMAIL"), os.Getenv("ADMIN_PASSWORD"), "admin")
	if err != nil {
		return fmt.Errorf("could not create admin; check credentials and whether the account already exists")
	}
	fmt.Println("admin created:", staff.ID)
	return nil
}
