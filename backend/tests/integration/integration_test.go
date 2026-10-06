package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/whitemodek/car-dealership-backend/backend/internal/api"
	"github.com/whitemodek/car-dealership-backend/backend/internal/database"
	"github.com/whitemodek/car-dealership-backend/backend/internal/domain"
	"github.com/whitemodek/car-dealership-backend/backend/internal/identity"
	"github.com/whitemodek/car-dealership-backend/backend/internal/inventory"
	"github.com/whitemodek/car-dealership-backend/backend/internal/leads"
	"github.com/whitemodek/car-dealership-backend/backend/internal/reservations"
	"github.com/whitemodek/car-dealership-backend/backend/internal/worker"
)

func uuid() string {
	token, err := domain.Token()
	if err != nil {
		panic(err)
	}
	return token[:8] + "-" + token[8:12] + "-4" + token[13:16] + "-8" + token[17:20] + "-" + token[20:32]
}
func setup(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to run real PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	control, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	token, err := domain.Token()
	if err != nil {
		t.Fatal(err)
	}
	schema := "test_" + token[:24]
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err = control.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		control.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := control.Exec(cleanup, "DROP SCHEMA "+identifier+" CASCADE"); err != nil {
			t.Error(err)
		}
		control.Close()
	})
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 20
	config.ConnConfig.RuntimeParams["search_path"] = schema
	config.ConnConfig.RuntimeParams["statement_timeout"] = "10000"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err = database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err = database.Migrate(ctx, pool); err != nil {
		t.Fatalf("migration replay: %v", err)
	}
	return pool
}
func request(t *testing.T, handler http.Handler, method, path, token, key string, body any, want int) []byte {
	t.Helper()
	var input []byte
	var err error
	if body != nil {
		input, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(input))
	r.RemoteAddr = "127.0.0.1:1234"
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s: got %d, want %d: %s", method, path, w.Code, want, w.Body.String())
	}
	if w.Header().Get("X-Request-ID") == "" {
		t.Fatal("request ID missing")
	}
	return w.Body.Bytes()
}
func decode[T any](t *testing.T, body []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestDealership(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	users, err := identity.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	password, err := domain.Token()
	if err != nil {
		t.Fatal(err)
	}
	admin, err := users.Create(ctx, "", "admin@example.test", password, "admin")
	if err != nil {
		t.Fatal(err)
	}
	manager, err := users.Create(ctx, admin.ID, "manager@example.test", password, "manager")
	if err != nil {
		t.Fatal(err)
	}
	handler, err := api.New(api.Options{Pool: pool, Logger: logger, ReservationDuration: 24 * time.Hour, Origins: []string{"https://dealer.example"}})
	if err != nil {
		t.Fatal(err)
	}
	handler = contractHandler(t, handler)
	login := func(email string) identity.Login {
		return decode[identity.Login](t, request(t, handler, "POST", "/api/v1/auth/login", "", "", map[string]string{"email": email, "password": password}, 200))
	}
	adminLogin := login(admin.Email)
	managerLogin := login(manager.Email)
	request(t, handler, "GET", "/health/ready", "", "", nil, 200)
	request(t, handler, "GET", "/api/v1/staff/leads", "", "", nil, 401)
	request(t, handler, "GET", "/api/v1/admin/staff", managerLogin.AccessToken, "", nil, 403)
	request(t, handler, "POST", "/api/v1/auth/login", "", "", map[string]string{"email": admin.Email, "password": "wrong-password"}, 401)
	model := decode[inventory.Model](t, request(t, handler, "POST", "/api/v1/admin/models", adminLogin.AccessToken, "", map[string]string{"make": "Toyota", "name": "Camry"}, 201))
	trim := decode[inventory.Trim](t, request(t, handler, "POST", "/api/v1/admin/trims", adminLogin.AccessToken, "", map[string]string{"model_id": model.ID, "name": "Premium"}, 201))
	request(t, handler, "GET", "/api/v1/models", "", "", nil, 200)
	request(t, handler, "GET", "/api/v1/models/"+model.ID+"/trims", "", "", nil, 200)
	otherPassword, err := domain.Token()
	if err != nil {
		t.Fatal(err)
	}
	request(t, handler, "POST", "/api/v1/admin/staff", adminLogin.AccessToken, "", map[string]string{"email": "second-manager@example.test", "password": otherPassword, "role": "manager"}, 201)
	request(t, handler, "GET", "/api/v1/admin/staff", adminLogin.AccessToken, "", nil, 200)
	stock := inventory.Store{Pool: pool}
	newCar := func(vin, condition string) inventory.Car {
		in := inventory.CreateCar{ModelID: model.ID, TrimID: &trim.ID, VIN: vin, Condition: condition, Year: 2024, MileageKM: 0, PriceMinor: 350000000, Currency: "RUB", Color: "white", Fuel: "petrol", Transmission: "automatic", Photos: []string{"https://cdn.example.test/car.jpg"}}
		if condition == "used" {
			in.MileageKM = 25000
		}
		car, err := stock.Create(ctx, manager.ID, in)
		if err != nil {
			t.Fatal(err)
		}
		published := "published"
		car, err = stock.Update(ctx, manager.ID, car.ID, inventory.UpdateCar{Version: car.Version, Publication: &published})
		if err != nil {
			t.Fatal(err)
		}
		return car
	}

	t.Run("catalog and sale flow", func(t *testing.T) {
		car := decode[inventory.Car](t, request(t, handler, "POST", "/api/v1/staff/cars", managerLogin.AccessToken, "", inventory.CreateCar{ModelID: model.ID, TrimID: &trim.ID, VIN: "1HGCM82633A004352", Condition: "used", Year: 2023, MileageKM: 20000, PriceMinor: 250000000, Currency: "RUB", Color: "black", Fuel: "petrol", Transmission: "automatic"}, 201))
		request(t, handler, "GET", "/api/v1/cars/"+car.ID, "", "", nil, 404)
		request(t, handler, "GET", "/api/v1/staff/cars/"+car.ID, managerLogin.AccessToken, "", nil, 200)
		request(t, handler, "GET", "/api/v1/staff/cars", managerLogin.AccessToken, "", nil, 200)
		car = decode[inventory.Car](t, request(t, handler, "PATCH", "/api/v1/staff/cars/"+car.ID, managerLogin.AccessToken, "", map[string]any{"version": car.Version, "publication": "published"}, 200))
		request(t, handler, "GET", "/api/v1/cars/"+car.ID, "", "", nil, 200)
		list := decode[struct {
			Items []inventory.Car `json:"items"`
		}](t, request(t, handler, "GET", "/api/v1/cars?condition=used&currency=RUB&sort=price_asc&q=Toyota", "", "", nil, 200))
		if len(list.Items) != 1 || list.Items[0].ID != car.ID {
			t.Fatal("catalog filters failed")
		}
		request(t, handler, "GET", "/api/v1/cars?sort=price_asc", "", "", nil, 422)
		request(t, handler, "GET", "/api/v1/cars?offset=-1", "", "", nil, 422)
		request(t, handler, "PATCH", "/api/v1/staff/cars/"+car.ID, managerLogin.AccessToken, "", map[string]any{"version": 1, "price_minor": 260000000}, 409)
		car = decode[inventory.Car](t, request(t, handler, "PATCH", "/api/v1/staff/cars/"+car.ID, managerLogin.AccessToken, "", map[string]any{"version": car.Version, "price_minor": 260000000}, 200))
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM price_history WHERE car_id=$1`, car.ID).Scan(&count); err != nil || count != 1 {
			t.Fatal("price history missing", err)
		}
		request(t, handler, "GET", "/api/v1/staff/cars/"+car.ID+"/prices", managerLogin.AccessToken, "", nil, 200)
		contact := leads.Contact{Name: "Customer", Phone: "+79990000000", Consent: true}
		booking := reservations.Create{CarID: car.ID, Contact: contact}
		key := uuid()
		response := request(t, handler, "POST", "/api/v1/reservations", "", key, booking, 201)
		if strings.Contains(string(response), "lead_id") || strings.Contains(string(response), contact.Phone) {
			t.Fatal("public booking leaks contacts")
		}
		reserved := decode[reservations.Reservation](t, response)
		replayed := decode[reservations.Reservation](t, request(t, handler, "POST", "/api/v1/reservations", "", key, booking, 200))
		if replayed.ID != reserved.ID {
			t.Fatal("idempotency failed")
		}
		booking.Name = "Different"
		request(t, handler, "POST", "/api/v1/reservations", "", key, booking, 409)
		public := decode[inventory.Car](t, request(t, handler, "GET", "/api/v1/cars/"+car.ID, "", "", nil, 200))
		if public.Available {
			t.Fatal("reserved car shown available")
		}
		request(t, handler, "PATCH", "/api/v1/staff/reservations/"+reserved.ID, managerLogin.AccessToken, "", map[string]string{"status": "confirmed"}, 200)
		request(t, handler, "GET", "/api/v1/staff/reservations", managerLogin.AccessToken, "", nil, 200)
		request(t, handler, "PATCH", "/api/v1/staff/cars/"+car.ID, managerLogin.AccessToken, "", map[string]any{"version": car.Version, "sale_status": "sold"}, 200)
		request(t, handler, "GET", "/api/v1/cars/"+car.ID, "", "", nil, 404)
		var status string
		if err := pool.QueryRow(ctx, `SELECT status FROM reservations WHERE id=$1`, reserved.ID).Scan(&status); err != nil || status != "completed" {
			t.Fatal("sale did not complete reservation", err)
		}
		var audit int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE entity_id=$1`, car.ID).Scan(&audit); err != nil || audit < 4 {
			t.Fatal("audit missing", err)
		}
		request(t, handler, "GET", "/api/v1/admin/audit?entity_id="+car.ID, adminLogin.AccessToken, "", nil, 200)
	})
	t.Run("one reservation under concurrency", func(t *testing.T) {
		car := newCar("1HGCM82633A004353", "new")
		store := reservations.Store{Pool: pool, Duration: 24 * time.Hour}
		start := make(chan struct{})
		result := make(chan error, 16)
		var wg sync.WaitGroup
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, _, err := store.Create(ctx, uuid(), reservations.Create{CarID: car.ID, Contact: leads.Contact{Name: "Buyer", Phone: "+79990000000", Consent: true}})
				result <- err
			}()
		}
		close(start)
		wg.Wait()
		close(result)
		success := 0
		for err := range result {
			if err == nil {
				success++
			} else if !errors.Is(err, domain.ErrConflict) {
				t.Fatal(err)
			}
		}
		if success != 1 {
			t.Fatalf("created %d reservations", success)
		}
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM reservations WHERE car_id=$1 AND status='active'`, car.ID).Scan(&count); err != nil || count != 1 {
			t.Fatal("database invariant failed", err)
		}
	})
	t.Run("same idempotency key concurrently", func(t *testing.T) {
		car := newCar("1HGCM82633A004354", "used")
		store := reservations.Store{Pool: pool, Duration: 24 * time.Hour}
		key := uuid()
		ids := make(chan string, 8)
		errs := make(chan error, 8)
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				v, _, err := store.Create(ctx, key, reservations.Create{CarID: car.ID, Contact: leads.Contact{Name: "Buyer", Phone: "+79990000000", Consent: true}})
				ids <- v.ID
				errs <- err
			}()
		}
		wg.Wait()
		close(ids)
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		first := ""
		for id := range ids {
			if first == "" {
				first = id
			}
			if id != first {
				t.Fatal("different reservations for same key")
			}
		}
	})
	t.Run("expiration without worker", func(t *testing.T) {
		car := newCar("1HGCM82633A004355", "new")
		store := reservations.Store{Pool: pool, Duration: 24 * time.Hour}
		in := reservations.Create{CarID: car.ID, Contact: leads.Contact{Name: "Buyer", Phone: "+79990000000", Consent: true}}
		old, _, err := store.Create(ctx, uuid(), in)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, `UPDATE reservations SET expires_at=now()-interval '1 minute' WHERE id=$1`, old.ID); err != nil {
			t.Fatal(err)
		}
		fresh, _, err := store.Create(ctx, uuid(), in)
		if err != nil || fresh.ID == old.ID {
			t.Fatal("expired booking blocks replacement", err)
		}
		var status string
		if err = pool.QueryRow(ctx, `SELECT status FROM reservations WHERE id=$1`, old.ID).Scan(&status); err != nil || status != "expired" {
			t.Fatal("old booking not expired", err)
		}
	})
	t.Run("worker expiration and cancellation", func(t *testing.T) {
		car := newCar("1HGCM82633A004357", "used")
		store := reservations.Store{Pool: pool, Duration: 24 * time.Hour}
		in := reservations.Create{CarID: car.ID, Contact: leads.Contact{Name: "Buyer", Phone: "+79990000000", Consent: true}}
		booking, _, err := store.Create(ctx, uuid(), in)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, `UPDATE reservations SET expires_at=now()-interval '1 minute' WHERE id=$1`, booking.ID); err != nil {
			t.Fatal(err)
		}
		if count, err := store.ExpireBatch(ctx); err != nil || count != 1 {
			t.Fatalf("worker expiration count=%d error=%v", count, err)
		}
		fresh, _, err := store.Create(ctx, uuid(), in)
		if err != nil {
			t.Fatal(err)
		}
		draft := "draft"
		updated, err := stock.Update(ctx, manager.ID, car.ID, inventory.UpdateCar{Version: car.Version, Publication: &draft})
		if err != nil {
			t.Fatal(err)
		}
		if updated.Available {
			t.Fatal("draft is bookable")
		}
		var status string
		if err = pool.QueryRow(ctx, `SELECT status FROM reservations WHERE id=$1`, fresh.ID).Scan(&status); err != nil || status != "cancelled" {
			t.Fatal("unpublishing did not cancel booking", err)
		}
		_, _, err = store.Create(ctx, uuid(), in)
		if !errors.Is(err, domain.ErrConflict) {
			t.Fatal("draft can be booked", err)
		}
	})
	t.Run("lead transitions", func(t *testing.T) {
		response := request(t, handler, "POST", "/api/v1/leads", "", "", leads.Create{Kind: "callback", Contact: leads.Contact{Name: "Customer", Phone: "+79990000000", Consent: true}}, 201)
		id := decode[struct {
			ID string `json:"id"`
		}](t, response).ID
		request(t, handler, "GET", "/api/v1/staff/leads", managerLogin.AccessToken, "", nil, 200)
		request(t, handler, "GET", "/api/v1/staff/leads/"+id, managerLogin.AccessToken, "", nil, 200)
		request(t, handler, "PATCH", "/api/v1/staff/leads/"+id, managerLogin.AccessToken, "", map[string]string{"assigned_to": manager.ID}, 200)
		request(t, handler, "PATCH", "/api/v1/staff/leads/"+id, managerLogin.AccessToken, "", map[string]string{"status": "qualified"}, 409)
		for _, status := range []string{"contacted", "qualified", "closed"} {
			request(t, handler, "PATCH", "/api/v1/staff/leads/"+id, managerLogin.AccessToken, "", map[string]string{"status": status}, 200)
		}
		request(t, handler, "POST", "/api/v1/leads", "", "", leads.Create{Kind: "callback", Contact: leads.Contact{Name: "Customer", Phone: "+79990000000"}}, 422)
	})
	t.Run("webhook signature and retry", func(t *testing.T) {
		secret := strings.Repeat("s", 32)
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			body, _ := io.ReadAll(r.Body)
			want := "sha256=" + worker.Signature(secret, r.Header.Get("X-Webhook-Timestamp"), body)
			if r.Header.Get("X-Webhook-Signature") != want || r.Header.Get("X-Event-ID") == "" {
				t.Error("invalid webhook signature")
			}
			if calls == 1 {
				w.WriteHeader(503)
			} else {
				w.WriteHeader(204)
			}
		}))
		defer server.Close()
		var event string
		if err := pool.QueryRow(ctx, `INSERT INTO outbox(kind,entity_id,available_at) VALUES('test.event',$1,now()-interval '1 day') RETURNING id::text`, admin.ID).Scan(&event); err != nil {
			t.Fatal(err)
		}
		w := worker.Worker{Pool: pool, Logger: logger, WebhookURL: server.URL, Secret: secret, Client: server.Client()}
		if _, err := w.DeliverOne(ctx); err != nil {
			t.Fatal(err)
		}
		var delivered bool
		if err := pool.QueryRow(ctx, `SELECT delivered_at IS NOT NULL FROM outbox WHERE id=$1`, event).Scan(&delivered); err != nil || delivered {
			t.Fatal("failed delivery marked successful", err)
		}
		if _, err := pool.Exec(ctx, `UPDATE outbox SET available_at=now()-interval '1 day' WHERE id=$1`, event); err != nil {
			t.Fatal(err)
		}
		if _, err := w.DeliverOne(ctx); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `SELECT delivered_at IS NOT NULL FROM outbox WHERE id=$1`, event).Scan(&delivered); err != nil || !delivered {
			t.Fatal("successful retry not recorded", err)
		}
	})
	t.Run("limits and security", func(t *testing.T) {
		for i := 0; i < 11; i++ {
			r := httptest.NewRequest("POST", "/api/v1/leads", strings.NewReader(`{"kind":"callback","name":"Visitor","phone":"+79990000000","consent":true}`))
			r.RemoteAddr = "192.0.2.5:1234"
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			want := 201
			if i == 10 {
				want = 429
			}
			if w.Code != want {
				t.Fatalf("rate limit: %d want %d", w.Code, want)
			}
		}
		for _, origin := range []string{"https://evil.example", "https://dealer.example"} {
			r := httptest.NewRequest("OPTIONS", "/api/v1/cars", nil)
			r.Header.Set("Origin", origin)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			want := 403
			if origin == "https://dealer.example" {
				want = 204
			}
			if w.Code != want {
				t.Fatalf("CORS: %d", w.Code)
			}
		}
		request(t, handler, "POST", "/api/v1/auth/logout", managerLogin.AccessToken, "", nil, 204)
		request(t, handler, "GET", "/api/v1/auth/me", managerLogin.AccessToken, "", nil, 401)
		managerLogin = login(manager.Email)
		request(t, handler, "POST", "/api/v1/admin/staff/"+manager.ID+"/disable", adminLogin.AccessToken, "", nil, 204)
		request(t, handler, "GET", "/api/v1/auth/me", managerLogin.AccessToken, "", nil, 401)
		request(t, handler, "POST", "/api/v1/admin/staff/"+admin.ID+"/disable", adminLogin.AccessToken, "", nil, 409)
		request(t, handler, "GET", "/api/v1/admin/metrics", adminLogin.AccessToken, "", nil, 200)
	})
	t.Run("database invariants", func(t *testing.T) {
		_, err := pool.Exec(ctx, `UPDATE cars SET price_minor=-1 WHERE vin='1HGCM82633A004353'`)
		if err == nil {
			t.Fatal("database accepted negative price")
		}
		other, err := stock.CreateModel(ctx, admin.ID, "Honda", "Accord")
		if err != nil {
			t.Fatal(err)
		}
		_, err = stock.Create(ctx, manager.ID, inventory.CreateCar{ModelID: other.ID, TrimID: &trim.ID, VIN: "1HGCM82633A004356", Condition: "new", Year: 2024, PriceMinor: 100, Currency: "RUB", Color: "white", Fuel: "petrol", Transmission: "automatic"})
		if err == nil {
			t.Fatal("accepted trim from another model")
		}
	})
	t.Run("oversized and unknown JSON", func(t *testing.T) {
		request(t, handler, "POST", "/api/v1/leads", "", "", map[string]any{"kind": "callback", "name": "Customer", "phone": "+79990000000", "consent": true, "unexpected": true}, 422)
		request(t, handler, "POST", "/api/v1/leads", "", "", map[string]any{"kind": "callback", "name": "Customer", "phone": "+79990000000", "consent": true, "message": strings.Repeat("x", 70<<10)}, 422)
	})
}

func TestDemoAndMigrationIntegrity(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	if err := database.SeedDemo(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM cars WHERE publication='published'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("demo cars %d: %v", count, err)
	}
	if err := database.SeedDemo(ctx, pool); err == nil {
		t.Fatal("demo seed overwrites nonempty catalog")
	}
	if _, err := pool.Exec(ctx, `UPDATE schema_migrations SET checksum='tampered'`); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx, pool); err == nil {
		t.Fatal("modified migration accepted")
	}
}
