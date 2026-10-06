package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/whitemodek/car-dealership-backend/backend/internal/domain"
	"github.com/whitemodek/car-dealership-backend/backend/internal/identity"
	"github.com/whitemodek/car-dealership-backend/backend/internal/inventory"
	"github.com/whitemodek/car-dealership-backend/backend/internal/leads"
	"github.com/whitemodek/car-dealership-backend/backend/internal/reservations"
)

type Options struct {
	Pool                *pgxpool.Pool
	Logger              *slog.Logger
	Origins             []string
	ReservationDuration time.Duration
	TrustedProxies      []netip.Prefix
}
type API struct {
	pool                                   *pgxpool.Pool
	logger                                 *slog.Logger
	identity                               identity.Store
	inventory                              inventory.Store
	leads                                  leads.Store
	reservations                           reservations.Store
	origins                                map[string]bool
	trustedProxies                         []netip.Prefix
	requests, failures, inflight, duration atomic.Int64
}
type endpoint func(http.ResponseWriter, *http.Request, identity.Staff) error

func New(options Options) (http.Handler, error) {
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	if err := ValidateOrigins(options.Origins); err != nil {
		return nil, err
	}
	id, err := identity.New(options.Pool)
	if err != nil {
		return nil, err
	}
	a := &API{pool: options.Pool, logger: options.Logger, identity: id, inventory: inventory.Store{Pool: options.Pool}, leads: leads.Store{Pool: options.Pool}, reservations: reservations.Store{Pool: options.Pool, Duration: options.ReservationDuration}, origins: map[string]bool{}}
	a.trustedProxies = options.TrustedProxies
	for _, origin := range options.Origins {
		a.origins[origin] = true
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		// Both database connectivity and the deployed schema are required.
		var ready bool
		err := a.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name='001_initial.sql')`).Scan(&ready)
		if err != nil || !ready {
			respond(w, 503, map[string]string{"status": "not_ready"})
			return
		}
		respond(w, 200, map[string]string{"status": "ok"})
	})
	a.route(mux, "POST /api/v1/auth/login", "", a.login)
	a.route(mux, "POST /api/v1/auth/logout", "staff", a.logout)
	a.route(mux, "GET /api/v1/auth/me", "staff", func(w http.ResponseWriter, r *http.Request, u identity.Staff) error { respond(w, 200, u); return nil })
	a.route(mux, "GET /api/v1/models", "", a.models)
	a.route(mux, "GET /api/v1/models/{id}/trims", "", a.trims)
	a.route(mux, "GET /api/v1/cars", "", a.cars(false))
	a.route(mux, "GET /api/v1/cars/{id}", "", a.car(false))
	a.route(mux, "POST /api/v1/leads", "", a.createLead)
	a.route(mux, "POST /api/v1/reservations", "", a.createReservation)
	a.route(mux, "GET /api/v1/staff/cars", "staff", a.cars(true))
	a.route(mux, "GET /api/v1/staff/cars/{id}", "staff", a.car(true))
	a.route(mux, "POST /api/v1/staff/cars", "staff", a.createCar)
	a.route(mux, "PATCH /api/v1/staff/cars/{id}", "staff", a.updateCar)
	a.route(mux, "GET /api/v1/staff/cars/{id}/prices", "staff", a.prices)
	a.route(mux, "POST /api/v1/admin/models", "admin", a.createModel)
	a.route(mux, "POST /api/v1/admin/trims", "admin", a.createTrim)
	a.route(mux, "GET /api/v1/staff/leads", "staff", a.listLeads)
	a.route(mux, "GET /api/v1/staff/leads/{id}", "staff", a.getLead)
	a.route(mux, "PATCH /api/v1/staff/leads/{id}", "staff", a.updateLead)
	a.route(mux, "GET /api/v1/staff/reservations", "staff", a.listReservations)
	a.route(mux, "PATCH /api/v1/staff/reservations/{id}", "staff", a.updateReservation)
	a.route(mux, "GET /api/v1/admin/staff", "admin", a.listStaff)
	a.route(mux, "POST /api/v1/admin/staff", "admin", a.createStaff)
	a.route(mux, "POST /api/v1/admin/staff/{id}/disable", "admin", a.disableStaff)
	a.route(mux, "GET /api/v1/admin/audit", "admin", a.audit)
	a.route(mux, "GET /api/v1/admin/metrics", "admin", a.metrics)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { a.failure(w, domain.ErrNotFound) })
	return a.middleware(mux), nil
}
func (a *API) route(mux *http.ServeMux, pattern, role string, handler endpoint) {
	mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		var user identity.Staff
		if id := r.PathValue("id"); id != "" && !domain.ValidID(id) {
			a.failure(w, domain.ErrInvalid)
			return
		}
		if role != "" {
			var err error
			user, err = a.identity.Authenticate(r.Context(), bearer(r))
			if err != nil {
				a.failure(w, err)
				return
			}
			if role == "admin" && user.Role != "admin" {
				a.failure(w, domain.ErrForbidden)
				return
			}
		}
		if err := handler(w, r, user); err != nil {
			a.failure(w, err)
		}
	})
}
func bearer(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(auth, "Bearer ")
}
func decode(r *http.Request, value any, required ...string) error {
	if content := strings.Split(r.Header.Get("Content-Type"), ";")[0]; strings.TrimSpace(content) != "application/json" {
		return domain.ErrInvalid
	}
	d := json.NewDecoder(r.Body)
	var raw json.RawMessage
	if err := d.Decode(&raw); err != nil {
		return domain.ErrInvalid
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return domain.ErrInvalid
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return domain.ErrInvalid
	}
	if len(required) > 0 {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			return domain.ErrInvalid
		}
		for _, field := range required {
			v, ok := object[field]
			if !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
				return domain.ErrInvalid
			}
		}
	}
	strict := json.NewDecoder(bytes.NewReader(raw))
	strict.DisallowUnknownFields()
	if err := strict.Decode(value); err != nil {
		return domain.ErrInvalid
	}
	return nil
}
func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if status != http.StatusNoContent {
		_ = json.NewEncoder(w).Encode(value)
	}
}
func (a *API) failure(w http.ResponseWriter, err error) {
	status, code := 500, "internal_error"
	switch {
	case errors.Is(err, domain.ErrInvalid):
		status, code = 422, "invalid_request"
	case errors.Is(err, domain.ErrNotFound):
		status, code = 404, "not_found"
	case errors.Is(err, domain.ErrConflict):
		status, code = 409, "conflict"
	case errors.Is(err, domain.ErrUnauthorized):
		status, code = 401, "unauthorized"
		w.Header().Set("WWW-Authenticate", "Bearer")
	case errors.Is(err, domain.ErrForbidden):
		status, code = 403, "forbidden"
	case errors.Is(err, context.DeadlineExceeded):
		status, code = 504, "request_timeout"
	default:
		var pg *pgconn.PgError
		if errors.As(err, &pg) {
			switch pg.Code {
			case "23505", "40001", "40P01":
				status, code = 409, "conflict"
			case "23503", "23514", "22P02", "22001":
				status, code = 422, "invalid_request"
			}
		}
	}
	if status >= 500 {
		a.logger.Error("request failed", "code", code)
	}
	respond(w, status, map[string]any{"error": map[string]string{"code": code}})
}

type recorder struct {
	http.ResponseWriter
	status int
}

func (w *recorder) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *recorder) Write(bytes []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	return w.ResponseWriter.Write(bytes)
}
func (a *API) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		a.requests.Add(1)
		a.inflight.Add(1)
		rw := &recorder{ResponseWriter: w}
		defer func() {
			if recover() != nil {
				a.logger.Error("request panic")
				if rw.status == 0 {
					a.failure(rw, errors.New("panic"))
				}
			}
			a.inflight.Add(-1)
			a.duration.Add(time.Since(start).Nanoseconds())
			if rw.status >= 500 {
				a.failures.Add(1)
			}
			// No query strings, bodies, credentials, customer contacts or arbitrary URL paths.
			a.logger.Info("http request", "method", r.Method, "route", r.Pattern, "status", rw.status, "duration_ms", time.Since(start).Milliseconds(), "request_id", rw.Header().Get("X-Request-ID"))
		}()
		token, err := domain.Token()
		if err != nil {
			a.failure(rw, err)
			return
		}
		rw.Header().Set("X-Request-ID", token[:24])
		rw.Header().Set("Cache-Control", "no-store")
		rw.Header().Set("X-Content-Type-Options", "nosniff")
		if origin := r.Header.Get("Origin"); origin != "" {
			rw.Header().Add("Vary", "Origin")
			if !a.origins[origin] {
				a.failure(rw, domain.ErrForbidden)
				return
			}
			rw.Header().Set("Access-Control-Allow-Origin", origin)
			rw.Header().Set("Access-Control-Expose-Headers", "X-Request-ID, Retry-After")
			if r.Method == http.MethodOptions {
				rw.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, OPTIONS")
				rw.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key")
				rw.Header().Set("Access-Control-Max-Age", "600")
				rw.WriteHeader(204)
				return
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		r.Body = http.MaxBytesReader(rw, r.Body, 64<<10)
		r = r.WithContext(ctx)
		next.ServeHTTP(rw, r)
	})
}
func (a *API) rate(w http.ResponseWriter, r *http.Request, category, key string, limit int) (bool, error) {
	if key == "" {
		host, err := clientIP(r, a.trustedProxies)
		if err != nil {
			return false, domain.ErrInvalid
		}
		key = host
	}
	var hits int
	err := a.pool.QueryRow(r.Context(), `INSERT INTO rate_limits(key_hash,bucket,hits) VALUES($1,date_trunc('minute',clock_timestamp()),1)
 ON CONFLICT(key_hash,bucket) DO UPDATE SET hits=LEAST(rate_limits.hits+1,$2) RETURNING hits`, identity.Hash(category+":"+key), limit+1).Scan(&hits)
	if err != nil {
		return false, err
	}
	if hits > limit {
		w.Header().Set("Retry-After", "60")
		respond(w, 429, map[string]any{"error": map[string]string{"code": "rate_limited"}})
		return false, nil
	}
	return true, nil
}

func clientIP(r *http.Request, trusted []netip.Prefix) (string, error) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return "", domain.ErrInvalid
	}
	current, err := netip.ParseAddr(host)
	if err != nil {
		return "", domain.ErrInvalid
	}
	current = current.Unmap()
	isTrusted := func(ip netip.Addr) bool {
		for _, prefix := range trusted {
			if prefix.Contains(ip) {
				return true
			}
		}
		return false
	}
	if !isTrusted(current) || r.Header.Get("X-Forwarded-For") == "" {
		return current.String(), nil
	}
	chain := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	if len(chain) > 10 {
		return "", domain.ErrInvalid
	}
	for i := len(chain) - 1; i >= 0 && isTrusted(current); i-- {
		current, err = netip.ParseAddr(strings.TrimSpace(chain[i]))
		if err != nil {
			return "", domain.ErrInvalid
		}
		current = current.Unmap()
	}
	return current.String(), nil
}
func page(r *http.Request) (int, int, error) {
	limit, offset := 20, 0
	var err error
	if v := r.URL.Query().Get("limit"); v != "" {
		limit, err = strconv.Atoi(v)
		if err != nil {
			return 0, 0, domain.ErrInvalid
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		offset, err = strconv.Atoi(v)
		if err != nil {
			return 0, 0, domain.ErrInvalid
		}
	}
	if limit < 1 || limit > 100 || offset < 0 || offset > 100000 {
		return 0, 0, domain.ErrInvalid
	}
	return limit, offset, nil
}
func (a *API) login(w http.ResponseWriter, r *http.Request, _ identity.Staff) error {
	if ok, err := a.rate(w, r, "login-ip", "", 25); !ok {
		return err
	}
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	if len(in.Email) > 254 {
		return domain.ErrUnauthorized
	}
	if ok, err := a.rate(w, r, "login-email", strings.ToLower(strings.TrimSpace(in.Email)), 10); !ok {
		return err
	}
	result, err := a.identity.Login(r.Context(), in.Email, in.Password)
	if err != nil {
		return err
	}
	respond(w, 200, result)
	return nil
}
func (a *API) logout(w http.ResponseWriter, r *http.Request, u identity.Staff) error {
	if err := a.identity.Logout(r.Context(), bearer(r), u.ID); err != nil {
		return err
	}
	respond(w, 204, nil)
	return nil
}
func (a *API) models(w http.ResponseWriter, r *http.Request, _ identity.Staff) error {
	v, err := a.inventory.Models(r.Context())
	if err != nil {
		return err
	}
	respond(w, 200, map[string]any{"items": v})
	return nil
}
func (a *API) trims(w http.ResponseWriter, r *http.Request, _ identity.Staff) error {
	v, err := a.inventory.Trims(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	respond(w, 200, map[string]any{"items": v})
	return nil
}
func (a *API) createModel(w http.ResponseWriter, r *http.Request, u identity.Staff) error {
	var in struct {
		Make string `json:"make"`
		Name string `json:"name"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	v, err := a.inventory.CreateModel(r.Context(), u.ID, in.Make, in.Name)
	if err != nil {
		return err
	}
	respond(w, 201, v)
	return nil
}
func (a *API) createTrim(w http.ResponseWriter, r *http.Request, u identity.Staff) error {
	var in struct {
		ModelID string `json:"model_id"`
		Name    string `json:"name"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	v, err := a.inventory.CreateTrim(r.Context(), u.ID, in.ModelID, in.Name)
	if err != nil {
		return err
	}
	respond(w, 201, v)
	return nil
}
func (a *API) cars(staff bool) endpoint {
	return func(w http.ResponseWriter, r *http.Request, _ identity.Staff) error {
		limit, offset, err := page(r)
		if err != nil {
			return err
		}
		q := r.URL.Query()
		f := inventory.Filter{Limit: limit, Offset: offset, Query: q.Get("q"), Condition: q.Get("condition"), ModelID: q.Get("model_id"), Currency: strings.ToUpper(q.Get("currency")), Sort: q.Get("sort")}
		if v := q.Get("min_price"); v != "" {
			f.MinPrice, err = strconv.ParseInt(v, 10, 64)
			if err != nil {
				return domain.ErrInvalid
			}
		}
		if v := q.Get("max_price"); v != "" {
			f.MaxPrice, err = strconv.ParseInt(v, 10, 64)
			if err != nil {
				return domain.ErrInvalid
			}
		}
		items, err := a.inventory.List(r.Context(), f, staff)
		if err != nil {
			return err
		}
		respond(w, 200, map[string]any{"items": items, "limit": limit, "offset": offset})
		return nil
	}
}
func (a *API) car(staff bool) endpoint {
	return func(w http.ResponseWriter, r *http.Request, _ identity.Staff) error {
		v, err := a.inventory.Get(r.Context(), r.PathValue("id"), staff)
		if err != nil {
			return err
		}
		respond(w, 200, v)
		return nil
	}
}
func (a *API) createCar(w http.ResponseWriter, r *http.Request, u identity.Staff) error {
	var in inventory.CreateCar
	if err := decode(r, &in, "mileage_km"); err != nil {
		return err
	}
	v, err := a.inventory.Create(r.Context(), u.ID, in)
	if err != nil {
		return err
	}
	w.Header().Set("Location", "/api/v1/staff/cars/"+v.ID)
	respond(w, 201, v)
	return nil
}
func (a *API) updateCar(w http.ResponseWriter, r *http.Request, u identity.Staff) error {
	var in inventory.UpdateCar
	if err := decode(r, &in); err != nil {
		return err
	}
	v, err := a.inventory.Update(r.Context(), u.ID, r.PathValue("id"), in)
	if err != nil {
		return err
	}
	respond(w, 200, v)
	return nil
}
func (a *API) prices(w http.ResponseWriter, r *http.Request, _ identity.Staff) error {
	id := r.PathValue("id")
	if _, err := a.inventory.Get(r.Context(), id, true); err != nil {
		return err
	}
	v, err := a.inventory.PriceHistory(r.Context(), id)
	if err != nil {
		return err
	}
	respond(w, 200, map[string]any{"items": v})
	return nil
}
func (a *API) createLead(w http.ResponseWriter, r *http.Request, _ identity.Staff) error {
	if ok, err := a.rate(w, r, "leads", "", 10); !ok {
		return err
	}
	var in leads.Create
	if err := decode(r, &in); err != nil {
		return err
	}
	id, err := a.leads.Create(r.Context(), in)
	if err != nil {
		return err
	}
	respond(w, 201, map[string]string{"id": id, "status": "new"})
	return nil
}
func (a *API) listLeads(w http.ResponseWriter, r *http.Request, _ identity.Staff) error {
	limit, offset, err := page(r)
	if err != nil {
		return err
	}
	v, err := a.leads.List(r.Context(), r.URL.Query().Get("status"), limit, offset)
	if err != nil {
		return err
	}
	respond(w, 200, map[string]any{"items": v, "limit": limit, "offset": offset})
	return nil
}
func (a *API) getLead(w http.ResponseWriter, r *http.Request, _ identity.Staff) error {
	v, err := a.leads.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	respond(w, 200, v)
	return nil
}
func (a *API) updateLead(w http.ResponseWriter, r *http.Request, u identity.Staff) error {
	var in leads.Update
	if err := decode(r, &in); err != nil {
		return err
	}
	v, err := a.leads.Update(r.Context(), u.ID, r.PathValue("id"), in)
	if err != nil {
		return err
	}
	respond(w, 200, v)
	return nil
}
func (a *API) createReservation(w http.ResponseWriter, r *http.Request, _ identity.Staff) error {
	if ok, err := a.rate(w, r, "reservations", "", 20); !ok {
		return err
	}
	var in reservations.Create
	if err := decode(r, &in); err != nil {
		return err
	}
	v, replay, err := a.reservations.Create(r.Context(), r.Header.Get("Idempotency-Key"), in)
	if err != nil {
		return err
	}
	status := 201
	if replay {
		status = 200
	}
	respond(w, status, map[string]any{"id": v.ID, "car_id": v.CarID, "status": v.Status, "expires_at": v.ExpiresAt})
	return nil
}
func (a *API) listReservations(w http.ResponseWriter, r *http.Request, _ identity.Staff) error {
	limit, offset, err := page(r)
	if err != nil {
		return err
	}
	v, err := a.reservations.List(r.Context(), limit, offset)
	if err != nil {
		return err
	}
	respond(w, 200, map[string]any{"items": v, "limit": limit, "offset": offset})
	return nil
}
func (a *API) updateReservation(w http.ResponseWriter, r *http.Request, u identity.Staff) error {
	var in struct {
		Status string `json:"status"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	v, err := a.reservations.Update(r.Context(), u.ID, r.PathValue("id"), in.Status)
	if err != nil {
		return err
	}
	respond(w, 200, v)
	return nil
}
func (a *API) listStaff(w http.ResponseWriter, r *http.Request, _ identity.Staff) error {
	v, err := a.identity.List(r.Context())
	if err != nil {
		return err
	}
	respond(w, 200, map[string]any{"items": v})
	return nil
}
func (a *API) createStaff(w http.ResponseWriter, r *http.Request, u identity.Staff) error {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	v, err := a.identity.Create(r.Context(), u.ID, in.Email, in.Password, in.Role)
	if err != nil {
		return err
	}
	respond(w, 201, v)
	return nil
}
func (a *API) disableStaff(w http.ResponseWriter, r *http.Request, u identity.Staff) error {
	if err := a.identity.Disable(r.Context(), u.ID, r.PathValue("id")); err != nil {
		return err
	}
	respond(w, 204, nil)
	return nil
}
func (a *API) audit(w http.ResponseWriter, r *http.Request, _ identity.Staff) error {
	limit, offset, err := page(r)
	if err != nil {
		return err
	}
	entity := r.URL.Query().Get("entity_id")
	var id any
	if entity != "" {
		if !domain.ValidID(entity) {
			return domain.ErrInvalid
		}
		id = entity
	}
	rows, err := a.pool.Query(r.Context(), `SELECT id,actor_id::text,action,entity_id::text,details,created_at FROM audit_log WHERE ($1::uuid IS NULL OR entity_id=$1) ORDER BY id DESC LIMIT $2 OFFSET $3`, id, limit, offset)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id int64
		var actor *string
		var action, entity string
		var details json.RawMessage
		var at time.Time
		if err = rows.Scan(&id, &actor, &action, &entity, &details, &at); err != nil {
			return err
		}
		items = append(items, map[string]any{"id": id, "actor_id": actor, "action": action, "entity_id": entity, "details": details, "created_at": at})
	}
	if err = rows.Err(); err != nil {
		return err
	}
	respond(w, 200, map[string]any{"items": items, "limit": limit, "offset": offset})
	return nil
}
func (a *API) metrics(w http.ResponseWriter, r *http.Request, _ identity.Staff) error {
	var pending, failed int64
	if err := a.pool.QueryRow(r.Context(), `SELECT count(*) FILTER (WHERE delivered_at IS NULL AND failed_at IS NULL),count(*) FILTER (WHERE failed_at IS NOT NULL) FROM outbox`).Scan(&pending, &failed); err != nil {
		return err
	}
	respond(w, 200, map[string]any{"requests_total": a.requests.Load(), "server_errors_total": a.failures.Load(), "inflight": a.inflight.Load(), "duration_seconds_total": float64(a.duration.Load()) / 1e9, "outbox_pending": pending, "outbox_failed": failed, "db_connections": a.pool.Stat().TotalConns()})
	return nil
}

func ValidateOrigins(origins []string) error {
	for _, origin := range origins {
		u, err := url.Parse(origin)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			return fmt.Errorf("invalid CORS origin")
		}
	}
	return nil
}
