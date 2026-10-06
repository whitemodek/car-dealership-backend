package worker

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/whitemodek/car-dealership-backend/backend/internal/reservations"
)

type Worker struct {
	Pool               *pgxpool.Pool
	Logger             *slog.Logger
	WebhookURL, Secret string
	Client             *http.Client
}
type Event struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	EntityID  string    `json:"entity_id"`
	CreatedAt time.Time `json:"created_at"`
	Attempts  int       `json:"-"`
}

func (w Worker) Run(ctx context.Context) error {
	if w.WebhookURL == "" {
		w.Logger.Warn("webhook not configured; events will remain pending")
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		runCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		if err := w.Tick(runCtx); err != nil && ctx.Err() == nil {
			w.Logger.Error("worker cycle failed")
		}
		cancel()
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
func (w Worker) Tick(ctx context.Context) error {
	n, err := (reservations.Store{Pool: w.Pool}).ExpireBatch(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		w.Logger.Info("reservations expired", "count", n)
	}
	if _, err = w.Pool.Exec(ctx, `DELETE FROM sessions WHERE expires_at<now(); DELETE FROM rate_limits WHERE bucket<now()-interval '1 day'; DELETE FROM outbox WHERE delivered_at<now()-interval '30 days'`); err != nil {
		return err
	}
	if w.WebhookURL == "" {
		return nil
	}
	for i := 0; i < 3; i++ {
		ok, err := w.DeliverOne(ctx)
		if err != nil {
			return err
		}
		if !ok {
			break
		}
	}
	return nil
}
func Signature(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "."))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
func (w Worker) DeliverOne(ctx context.Context) (bool, error) {
	// A short database lease allows multiple workers; HTTP happens outside a transaction.
	var event Event
	err := w.Pool.QueryRow(ctx, `WITH candidate AS (
 SELECT id FROM outbox WHERE delivered_at IS NULL AND failed_at IS NULL AND available_at<=now() ORDER BY available_at,id LIMIT 1 FOR UPDATE SKIP LOCKED
 ) UPDATE outbox o SET available_at=now()+interval '2 minutes',attempts=attempts+1 FROM candidate c WHERE o.id=c.id
 RETURNING o.id::text,o.kind,o.entity_id::text,o.created_at,o.attempts`).Scan(&event.ID, &event.Kind, &event.EntityID, &event.CreatedAt, &event.Attempts)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	body, err := json.Marshal(event)
	if err != nil {
		return true, err
	}
	deliveryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(deliveryCtx, http.MethodPost, w.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return true, err
	}
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Event-ID", event.ID)
	req.Header.Set("X-Webhook-Timestamp", timestamp)
	req.Header.Set("X-Webhook-Signature", "sha256="+Signature(w.Secret, timestamp, body))
	client := w.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	response, sendErr := client.Do(req)
	delivered := sendErr == nil && response.StatusCode >= 200 && response.StatusCode < 300
	if response != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		response.Body.Close()
	}
	if delivered {
		_, err = w.Pool.Exec(ctx, `UPDATE outbox SET delivered_at=now() WHERE id=$1 AND attempts=$2 AND delivered_at IS NULL`, event.ID, event.Attempts)
		return true, err
	}
	if event.Attempts >= 8 {
		_, err = w.Pool.Exec(ctx, `UPDATE outbox SET failed_at=now() WHERE id=$1 AND attempts=$2 AND delivered_at IS NULL`, event.ID, event.Attempts)
		w.Logger.Error("webhook delivery exhausted", "event_id", event.ID)
	} else {
		delay := int64(1<<event.Attempts) * 5
		_, err = w.Pool.Exec(ctx, `UPDATE outbox SET available_at=now()+($3::bigint * interval '1 second') WHERE id=$1 AND attempts=$2 AND delivered_at IS NULL`, event.ID, event.Attempts, delay)
		w.Logger.Warn("webhook delivery will retry", "event_id", event.ID, "attempt", event.Attempts)
	}
	if err != nil {
		return true, fmt.Errorf("record delivery: %w", err)
	}
	return true, nil
}
