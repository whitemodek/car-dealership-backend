package reservations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/whitemodek/car-dealership-backend/backend/internal/domain"
	"github.com/whitemodek/car-dealership-backend/backend/internal/leads"
)

type Store struct {
	Pool     *pgxpool.Pool
	Duration time.Duration
}
type Create struct {
	CarID string `json:"car_id"`
	leads.Contact
}
type Reservation struct {
	ID        string    `json:"id"`
	CarID     string    `json:"car_id"`
	LeadID    string    `json:"lead_id"`
	Status    string    `json:"status"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

const selectReservation = `SELECT id::text,car_id::text,lead_id::text,CASE WHEN status IN ('active','confirmed') AND expires_at<=clock_timestamp() THEN 'expired' ELSE status END,expires_at,created_at FROM reservations `

func scan(row pgx.Row) (Reservation, error) {
	var v Reservation
	err := row.Scan(&v.ID, &v.CarID, &v.LeadID, &v.Status, &v.ExpiresAt, &v.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, domain.ErrNotFound
	}
	return v, err
}
func ExpireCar(ctx context.Context, tx pgx.Tx, car string) (int, error) {
	rows, err := tx.Query(ctx, `UPDATE reservations SET status='expired',updated_at=now() WHERE car_id=$1 AND status IN ('active','confirmed') AND expires_at<=clock_timestamp() RETURNING id::text`, car)
	if err != nil {
		return 0, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return 0, err
	}
	for _, id := range ids {
		if err = domain.Audit(ctx, tx, "", "reservation.expired", id, map[string]any{}); err != nil {
			return 0, err
		}
		if err = domain.Event(ctx, tx, "reservation.expired", id); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}
func (s Store) Create(ctx context.Context, key string, in Create) (Reservation, bool, error) {
	key = strings.ToLower(key)
	in.CarID = strings.ToLower(in.CarID)
	if !domain.ValidID(key) || !domain.ValidID(in.CarID) || s.Duration <= 0 {
		return Reservation{}, false, domain.ErrInvalid
	}
	if err := in.Contact.Validate(); err != nil {
		return Reservation{}, false, err
	}
	body, err := json.Marshal(in)
	if err != nil {
		return Reservation{}, false, err
	}
	hash := sha256.Sum256(body)
	fingerprint := hex.EncodeToString(hash[:])
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Reservation{}, false, err
	}
	defer tx.Rollback(ctx)
	// Serializes identical keys, including requests for different cars.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,42))`, key); err != nil {
		return Reservation{}, false, err
	}
	var previous string
	err = tx.QueryRow(ctx, `SELECT request_hash FROM reservations WHERE idempotency_key=$1`, key).Scan(&previous)
	if err == nil {
		if previous != fingerprint {
			return Reservation{}, false, domain.ErrConflict
		}
		v, err := scan(tx.QueryRow(ctx, selectReservation+`WHERE idempotency_key=$1`, key))
		if err != nil {
			return v, false, err
		}
		// Replays must also report expiration accurately while worker may lag.
		if _, err = tx.Exec(ctx, `SELECT id FROM cars WHERE id=$1 FOR UPDATE`, v.CarID); err != nil {
			return v, false, err
		}
		if _, err = ExpireCar(ctx, tx, v.CarID); err != nil {
			return v, false, err
		}
		v, err = scan(tx.QueryRow(ctx, selectReservation+`WHERE id=$1`, v.ID))
		if err != nil {
			return v, false, err
		}
		return v, true, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Reservation{}, false, err
	}
	var publication, sale string
	err = tx.QueryRow(ctx, `SELECT publication,sale_status FROM cars WHERE id=$1 FOR UPDATE`, in.CarID).Scan(&publication, &sale)
	if errors.Is(err, pgx.ErrNoRows) {
		return Reservation{}, false, domain.ErrNotFound
	}
	if err != nil {
		return Reservation{}, false, err
	}
	if publication != "published" || sale != "available" {
		return Reservation{}, false, domain.ErrConflict
	}
	if _, err = ExpireCar(ctx, tx, in.CarID); err != nil {
		return Reservation{}, false, err
	}
	var busy bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM reservations WHERE car_id=$1 AND status IN ('active','confirmed'))`, in.CarID).Scan(&busy); err != nil {
		return Reservation{}, false, err
	}
	if busy {
		return Reservation{}, false, domain.ErrConflict
	}
	lead, err := leads.Insert(ctx, tx, leads.Create{CarID: &in.CarID, Kind: "purchase", Contact: in.Contact})
	if err != nil {
		return Reservation{}, false, err
	}
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO reservations(car_id,lead_id,idempotency_key,request_hash,expires_at) VALUES($1,$2,$3,$4,clock_timestamp()+($5::bigint * interval '1 second')) RETURNING id::text`, in.CarID, lead, key, fingerprint, int64(s.Duration/time.Second)).Scan(&id)
	if err != nil {
		return Reservation{}, false, err
	}
	if err = domain.Audit(ctx, tx, "", "reservation.created", id, map[string]any{}); err != nil {
		return Reservation{}, false, err
	}
	if err = domain.Event(ctx, tx, "reservation.created", id); err != nil {
		return Reservation{}, false, err
	}
	v, err := scan(tx.QueryRow(ctx, selectReservation+`WHERE id=$1`, id))
	if err != nil {
		return v, false, err
	}
	return v, false, tx.Commit(ctx)
}
func (s Store) List(ctx context.Context, limit, offset int) ([]Reservation, error) {
	rows, err := s.Pool.Query(ctx, selectReservation+`ORDER BY created_at DESC,id DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Reservation{}
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, v)
	}
	return items, rows.Err()
}
func (s Store) Update(ctx context.Context, actor, id, status string) (Reservation, error) {
	if status != "confirmed" && status != "cancelled" {
		return Reservation{}, domain.ErrInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Reservation{}, err
	}
	defer tx.Rollback(ctx)
	var car string
	err = tx.QueryRow(ctx, `SELECT car_id::text FROM reservations WHERE id=$1`, id).Scan(&car)
	if errors.Is(err, pgx.ErrNoRows) {
		return Reservation{}, domain.ErrNotFound
	}
	if err != nil {
		return Reservation{}, err
	}
	if _, err = tx.Exec(ctx, `SELECT id FROM cars WHERE id=$1 FOR UPDATE`, car); err != nil {
		return Reservation{}, err
	}
	if _, err = ExpireCar(ctx, tx, car); err != nil {
		return Reservation{}, err
	}
	v, err := scan(tx.QueryRow(ctx, selectReservation+`WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return v, err
	}
	if v.Status == status {
		return v, tx.Commit(ctx)
	}
	if v.Status != "active" && !(v.Status == "confirmed" && status == "cancelled") {
		return v, domain.ErrConflict
	}
	if _, err = tx.Exec(ctx, `UPDATE reservations SET status=$2,updated_at=now() WHERE id=$1`, id, status); err != nil {
		return v, err
	}
	if err = domain.Audit(ctx, tx, actor, "reservation."+status, id, map[string]any{}); err != nil {
		return v, err
	}
	if err = domain.Event(ctx, tx, "reservation."+status, id); err != nil {
		return v, err
	}
	v.Status = status
	return v, tx.Commit(ctx)
}

func (s Store) ExpireBatch(ctx context.Context) (int, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT c.id::text FROM cars c WHERE EXISTS(SELECT 1 FROM reservations r WHERE r.car_id=c.id AND r.status IN ('active','confirmed') AND r.expires_at<=clock_timestamp()) ORDER BY c.id LIMIT 100 FOR UPDATE OF c SKIP LOCKED`)
	if err != nil {
		return 0, err
	}
	cars := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		cars = append(cars, id)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return 0, err
	}
	count := 0
	for _, car := range cars {
		n, err := ExpireCar(ctx, tx, car)
		if err != nil {
			return 0, err
		}
		count += n
	}
	return count, tx.Commit(ctx)
}
