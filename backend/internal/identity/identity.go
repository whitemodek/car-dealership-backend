package identity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/mail"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/whitemodek/car-dealership-backend/backend/internal/domain"
	"golang.org/x/crypto/bcrypt"
)

type Store struct {
	Pool      *pgxpool.Pool
	DummyHash []byte
}
type Staff struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
}
type Login struct {
	AccessToken string    `json:"access_token"`
	TokenType   string    `json:"token_type"`
	ExpiresAt   time.Time `json:"expires_at"`
	Staff       Staff     `json:"staff"`
}

func New(pool *pgxpool.Pool) (Store, error) {
	// Unknown accounts still incur the same bcrypt cost as a real login.
	dummy, err := bcrypt.GenerateFromPassword([]byte("unused-dummy-account-password"), 12)
	return Store{Pool: pool, DummyHash: dummy}, err
}
func ValidEmail(email string) bool {
	address, err := mail.ParseAddress(email)
	return err == nil && address.Address == email && len(email) <= 254 && strings.Contains(email, "@")
}
func Hash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
func (s Store) Create(ctx context.Context, actor, email, password, role string) (Staff, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if !ValidEmail(email) || len([]rune(password)) < 12 || len(password) > 72 || (role != "admin" && role != "manager") {
		return Staff{}, domain.ErrInvalid
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return Staff{}, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Staff{}, err
	}
	defer tx.Rollback(ctx)
	var v Staff
	err = tx.QueryRow(ctx, `INSERT INTO staff(email,password_hash,role) VALUES($1,$2,$3) RETURNING id::text,email,role,active,created_at`, email, string(hash), role).Scan(&v.ID, &v.Email, &v.Role, &v.Active, &v.CreatedAt)
	if err != nil {
		return v, err
	}
	if err = domain.Audit(ctx, tx, actor, "staff.created", v.ID, map[string]any{"role": role}); err != nil {
		return v, err
	}
	return v, tx.Commit(ctx)
}
func (s Store) Login(ctx context.Context, email, password string) (Login, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if len(password) > 72 || len(email) > 254 {
		return Login{}, domain.ErrUnauthorized
	}
	var v Staff
	var hash string
	err := s.Pool.QueryRow(ctx, `SELECT id::text,email,role,active,created_at,password_hash FROM staff WHERE email=$1`, email).Scan(&v.ID, &v.Email, &v.Role, &v.Active, &v.CreatedAt, &hash)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Login{}, err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		_ = bcrypt.CompareHashAndPassword(s.DummyHash, []byte(password))
		return Login{}, domain.ErrUnauthorized
	}
	if err = bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil || !v.Active {
		return Login{}, domain.ErrUnauthorized
	}
	token, err := domain.Token()
	if err != nil {
		return Login{}, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Login{}, err
	}
	defer tx.Rollback(ctx)
	// Shared lock serializes session creation with account deactivation.
	var active bool
	if err = tx.QueryRow(ctx, `SELECT active FROM staff WHERE id=$1 FOR SHARE`, v.ID).Scan(&active); err != nil {
		return Login{}, err
	}
	if !active {
		return Login{}, domain.ErrUnauthorized
	}
	var expires time.Time
	if err = tx.QueryRow(ctx, `INSERT INTO sessions(token_hash,staff_id,expires_at) VALUES($1,$2,now()+interval '12 hours') RETURNING expires_at`, Hash(token), v.ID).Scan(&expires); err != nil {
		return Login{}, err
	}
	if err = domain.Audit(ctx, tx, v.ID, "session.created", v.ID, map[string]any{}); err != nil {
		return Login{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Login{}, err
	}
	return Login{AccessToken: token, TokenType: "Bearer", ExpiresAt: expires, Staff: v}, nil
}
func (s Store) Authenticate(ctx context.Context, token string) (Staff, error) {
	if len(token) != 64 {
		return Staff{}, domain.ErrUnauthorized
	}
	if _, err := hex.DecodeString(token); err != nil {
		return Staff{}, domain.ErrUnauthorized
	}
	var v Staff
	err := s.Pool.QueryRow(ctx, `SELECT u.id::text,u.email,u.role,u.active,u.created_at FROM sessions s JOIN staff u ON u.id=s.staff_id WHERE s.token_hash=$1 AND s.expires_at>now() AND u.active`, Hash(token)).Scan(&v.ID, &v.Email, &v.Role, &v.Active, &v.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, domain.ErrUnauthorized
	}
	return v, err
}
func (s Store) Logout(ctx context.Context, token, actor string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `DELETE FROM sessions WHERE token_hash=$1 AND staff_id=$2`, Hash(token), actor); err != nil {
		return err
	}
	if err = domain.Audit(ctx, tx, actor, "session.revoked", actor, map[string]any{}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s Store) List(ctx context.Context) ([]Staff, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id::text,email,role,active,created_at FROM staff ORDER BY created_at,id LIMIT 500`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Staff{}
	for rows.Next() {
		var v Staff
		if err = rows.Scan(&v.ID, &v.Email, &v.Role, &v.Active, &v.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, v)
	}
	return items, rows.Err()
}
func (s Store) Disable(ctx context.Context, actor, id string) error {
	if id == actor {
		return domain.ErrConflict
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT id FROM staff WHERE role='admin' ORDER BY id FOR UPDATE`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var lockedID string
		if err = rows.Scan(&lockedID); err != nil {
			rows.Close()
			return err
		}
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	var role string
	var active bool
	err = tx.QueryRow(ctx, `SELECT role,active FROM staff WHERE id=$1 FOR UPDATE`, id).Scan(&role, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	if err != nil {
		return err
	}
	if !active {
		return nil
	}
	if role == "admin" {
		var count int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM staff WHERE role='admin' AND active`).Scan(&count); err != nil {
			return err
		}
		if count <= 1 {
			return domain.ErrConflict
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE staff SET active=false WHERE id=$1`, id); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM sessions WHERE staff_id=$1`, id); err != nil {
		return err
	}
	if err = domain.Audit(ctx, tx, actor, "staff.disabled", id, map[string]any{}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
