package integration

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/whitemodek/car-dealership-backend/backend/internal/api"
)

// Optional, reproducible local HTTP/database smoke load, not a capacity claim.
func TestCatalogLoad(t *testing.T) {
	if os.Getenv("LOAD_TEST") != "1" {
		t.Skip("set LOAD_TEST=1 to run HTTP load smoke")
	}
	pool := setup(t)
	ctx := context.Background()
	var model string
	if err := pool.QueryRow(ctx, `INSERT INTO models(make,name) VALUES('Toyota','Camry') RETURNING id::text`).Scan(&model); err != nil {
		t.Fatal(err)
	}
	_, err := pool.Exec(ctx, `INSERT INTO cars(model_id,vin,condition,year,mileage_km,price_minor,currency,color,fuel,transmission,publication)
 SELECT $1,'JTDZZZ000'||lpad(n::text,8,'0'),CASE WHEN n%2=0 THEN 'new' ELSE 'used' END,2024,n*1000,200000000+n*10000,'RUB','white','petrol','automatic','published' FROM generate_series(1,100) n`, model)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := api.New(api.Options{Pool: pool, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)), ReservationDuration: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	client := server.Client()
	client.Timeout = 5 * time.Second
	const concurrency = 8
	const perWorker = 50
	latencies := make(chan time.Duration, concurrency*perWorker)
	errs := make(chan error, concurrency*perWorker)
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perWorker; j++ {
				before := time.Now()
				response, err := client.Get(server.URL + "/api/v1/cars?currency=RUB&sort=price_asc&limit=20")
				if err != nil {
					errs <- err
					continue
				}
				_, err = io.Copy(io.Discard, response.Body)
				response.Body.Close()
				if response.StatusCode != http.StatusOK {
					t.Errorf("load request: %d", response.StatusCode)
				}
				if err != nil {
					errs <- err
				}
				latencies <- time.Since(before)
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)
	close(latencies)
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	values := []time.Duration{}
	for value := range latencies {
		values = append(values, value)
	}
	if len(values) != concurrency*perWorker {
		t.Fatalf("only %d completed requests", len(values))
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	t.Logf("HTTP catalog load: requests=%d concurrency=%d elapsed=%s p50=%s p95=%s p99=%s", len(values), concurrency, elapsed.Round(time.Millisecond), values[len(values)*50/100].Round(time.Microsecond), values[len(values)*95/100].Round(time.Microsecond), values[len(values)*99/100].Round(time.Microsecond))
}
