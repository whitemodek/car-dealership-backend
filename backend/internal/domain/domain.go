package domain

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"regexp"

	"github.com/jackc/pgx/v5"
)

var (
	ErrInvalid      = errors.New("invalid request")
	ErrNotFound     = errors.New("not found")
	ErrConflict     = errors.New("conflict")
	ErrUnauthorized = errors.New("unauthorized")
	ErrForbidden    = errors.New("forbidden")
	uuidPattern     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

func ValidID(id string) bool { return uuidPattern.MatchString(id) }

func Token() (string, error) {
	var bytes [32]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}

func Audit(ctx context.Context, tx pgx.Tx, actor, action, entity string, details any) error {
	var actorID any
	if actor != "" {
		actorID = actor
	}
	_, err := tx.Exec(ctx, `INSERT INTO audit_log(actor_id,action,entity_id,details) VALUES($1,$2,$3,$4)`, actorID, action, entity, details)
	return err
}

func Event(ctx context.Context, tx pgx.Tx, kind, entity string) error {
	_, err := tx.Exec(ctx, `INSERT INTO outbox(kind,entity_id) VALUES($1,$2)`, kind, entity)
	return err
}
