package leads

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/whitemodek/car-dealership-backend/backend/internal/domain"
	"github.com/whitemodek/car-dealership-backend/backend/internal/identity"
	"github.com/whitemodek/car-dealership-backend/backend/internal/inventory"
)

type Store struct{ Pool *pgxpool.Pool }
type Contact struct {
	Name    string `json:"name"`
	Phone   string `json:"phone"`
	Email   string `json:"email"`
	Message string `json:"message"`
	Consent bool   `json:"consent"`
}
type Create struct {
	Contact
	CarID *string `json:"car_id"`
	Kind  string  `json:"kind"`
}
type Lead struct {
	ID         string    `json:"id"`
	CarID      *string   `json:"car_id"`
	Kind       string    `json:"kind"`
	Name       string    `json:"name"`
	Phone      string    `json:"phone"`
	Email      string    `json:"email"`
	Message    string    `json:"message"`
	Status     string    `json:"status"`
	AssignedTo *string   `json:"assigned_to"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

var phonePattern = regexp.MustCompile(`^\+?[0-9 ()-]{5,30}$`)

func (c *Contact) Validate() error {
	c.Name = strings.TrimSpace(c.Name)
	c.Phone = strings.TrimSpace(c.Phone)
	c.Email = strings.ToLower(strings.TrimSpace(c.Email))
	if !c.Consent || !inventory.ValidText(c.Name, 100) || !phonePattern.MatchString(c.Phone) || len([]rune(c.Message)) > 2000 || strings.ContainsRune(c.Message, '\x00') || (c.Email != "" && !identity.ValidEmail(c.Email)) {
		return domain.ErrInvalid
	}
	digits := 0
	for _, char := range c.Phone {
		if char >= '0' && char <= '9' {
			digits++
		}
	}
	if digits < 5 || digits > 15 {
		return domain.ErrInvalid
	}
	return nil
}
func Insert(ctx context.Context, tx pgx.Tx, in Create) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `INSERT INTO leads(car_id,kind,customer_name,phone,email,message) VALUES($1,$2,$3,$4,$5,$6) RETURNING id::text`, in.CarID, in.Kind, in.Name, in.Phone, in.Email, in.Message).Scan(&id)
	if err != nil {
		return "", err
	}
	if err = domain.Event(ctx, tx, "lead.created", id); err != nil {
		return "", err
	}
	return id, nil
}
func (s Store) Create(ctx context.Context, in Create) (string, error) {
	if err := in.Contact.Validate(); err != nil {
		return "", err
	}
	if (in.CarID != nil && !domain.ValidID(*in.CarID)) || (in.Kind != "purchase" && in.Kind != "callback" && in.Kind != "test_drive") || (in.Kind != "callback" && in.CarID == nil) {
		return "", domain.ErrInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	if in.CarID != nil {
		var id string
		err = tx.QueryRow(ctx, `SELECT id::text FROM cars WHERE id=$1 AND publication='published' AND sale_status='available' FOR SHARE`, *in.CarID).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", domain.ErrNotFound
		}
		if err != nil {
			return "", err
		}
	}
	id, err := Insert(ctx, tx, in)
	if err != nil {
		return "", err
	}
	return id, tx.Commit(ctx)
}

const leadSelect = `SELECT id::text,car_id::text,kind,customer_name,phone,email,message,status,assigned_to::text,created_at,updated_at FROM leads `

func scan(row pgx.Row) (Lead, error) {
	var l Lead
	err := row.Scan(&l.ID, &l.CarID, &l.Kind, &l.Name, &l.Phone, &l.Email, &l.Message, &l.Status, &l.AssignedTo, &l.CreatedAt, &l.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return l, domain.ErrNotFound
	}
	return l, err
}
func (s Store) List(ctx context.Context, status string, limit, offset int) ([]Lead, error) {
	if status != "" && status != "new" && status != "contacted" && status != "qualified" && status != "closed" {
		return nil, domain.ErrInvalid
	}
	rows, err := s.Pool.Query(ctx, leadSelect+`WHERE ($1='' OR status=$1) ORDER BY created_at DESC,id DESC LIMIT $2 OFFSET $3`, status, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Lead{}
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, v)
	}
	return items, rows.Err()
}
func (s Store) Get(ctx context.Context, id string) (Lead, error) {
	return scan(s.Pool.QueryRow(ctx, leadSelect+`WHERE id=$1`, id))
}
func AllowedTransition(from, to string) bool {
	if from == to {
		return true
	}
	switch from {
	case "new":
		return to == "contacted" || to == "closed"
	case "contacted":
		return to == "qualified" || to == "closed"
	case "qualified":
		return to == "closed"
	}
	return false
}

type Update struct {
	Status     *string `json:"status"`
	AssignedTo *string `json:"assigned_to"`
	Unassign   bool    `json:"unassign"`
}

func (s Store) Update(ctx context.Context, actor, id string, in Update) (Lead, error) {
	if (in.Status == nil && in.AssignedTo == nil && !in.Unassign) || (in.Unassign && in.AssignedTo != nil) || (in.AssignedTo != nil && !domain.ValidID(*in.AssignedTo)) {
		return Lead{}, domain.ErrInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Lead{}, err
	}
	defer tx.Rollback(ctx)
	lead, err := scan(tx.QueryRow(ctx, leadSelect+`WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return Lead{}, err
	}
	if in.Status != nil && !AllowedTransition(lead.Status, *in.Status) {
		return Lead{}, domain.ErrConflict
	}
	if in.AssignedTo != nil {
		var active bool
		err = tx.QueryRow(ctx, `SELECT active FROM staff WHERE id=$1 FOR SHARE`, *in.AssignedTo).Scan(&active)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && !active) {
			return Lead{}, domain.ErrInvalid
		}
		if err != nil {
			return Lead{}, err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE leads SET status=COALESCE($2,status),assigned_to=CASE WHEN $4 THEN NULL ELSE COALESCE($3,assigned_to) END,updated_at=now() WHERE id=$1`, id, in.Status, in.AssignedTo, in.Unassign)
	if err != nil {
		return Lead{}, err
	}
	updated, err := scan(tx.QueryRow(ctx, leadSelect+`WHERE id=$1`, id))
	if err != nil {
		return Lead{}, err
	}
	if err = domain.Audit(ctx, tx, actor, "lead.updated", id, map[string]any{"status": updated.Status, "assigned_to": updated.AssignedTo}); err != nil {
		return Lead{}, err
	}
	if err = domain.Event(ctx, tx, "lead.updated", id); err != nil {
		return Lead{}, err
	}
	return updated, tx.Commit(ctx)
}
