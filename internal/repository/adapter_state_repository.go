package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"adapter-matrix/internal/events"

	"github.com/google/uuid"
)

const (
	statusPending = "pending"
	statusSent    = "sent"
	statusFailed  = "failed"
)

var tableNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_.]+$`)

func IsValidTableName(name string) bool {
	return tableNamePattern.MatchString(name)
}

type AdapterStateRepository struct {
	db          *sql.DB
	outboxTable string
}

func NewAdapterStateRepository(db *sql.DB, outboxTable string) (*AdapterStateRepository, error) {
	if db == nil {
		return nil, errors.New("db is required")
	}
	if outboxTable == "" {
		return nil, errors.New("outbox table is required")
	}
	if !IsValidTableName(outboxTable) {
		return nil, errors.New("outbox table name contains invalid characters")
	}
	return &AdapterStateRepository{db: db, outboxTable: outboxTable}, nil
}

func (r *AdapterStateRepository) ClaimEvent(ctx context.Context, eventID string) (int, bool, error) {
	parsed, err := uuid.Parse(eventID)
	if err != nil {
		return 0, false, err
	}

	query := `
		INSERT INTO adapter_event_state (event_id, attempts, status, last_error, updated_at)
		VALUES ($1, 1, $2, NULL, $3)
		ON CONFLICT (event_id) DO UPDATE
		SET attempts = adapter_event_state.attempts + 1,
			status = $2,
			updated_at = $3
		WHERE adapter_event_state.status NOT IN ($4, $5)
		RETURNING attempts
	`
	row := r.db.QueryRowContext(ctx, query, parsed, statusPending, time.Now().UTC(), statusSent, statusFailed)
	var attempts int
	if err := row.Scan(&attempts); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return attempts, true, nil
}

func (r *AdapterStateRepository) MarkSent(ctx context.Context, eventID string) error {
	parsed, err := uuid.Parse(eventID)
	if err != nil {
		return err
	}
	query := `
		UPDATE adapter_event_state
		SET status = $2,
			last_error = NULL,
			updated_at = $3
		WHERE event_id = $1
	`
	_, err = r.db.ExecContext(ctx, query, parsed, statusSent, time.Now().UTC())
	return err
}

func (r *AdapterStateRepository) MarkRetry(ctx context.Context, eventID, lastError string) error {
	parsed, err := uuid.Parse(eventID)
	if err != nil {
		return err
	}
	query := `
		UPDATE adapter_event_state
		SET status = $2,
			last_error = $3,
			updated_at = $4
		WHERE event_id = $1
	`
	_, err = r.db.ExecContext(ctx, query, parsed, statusPending, lastError, time.Now().UTC())
	return err
}

func (r *AdapterStateRepository) MarkFailed(ctx context.Context, eventID, lastError string) error {
	parsed, err := uuid.Parse(eventID)
	if err != nil {
		return err
	}
	query := `
		UPDATE adapter_event_state
		SET status = $2,
			last_error = $3,
			updated_at = $4
		WHERE event_id = $1
	`
	_, err = r.db.ExecContext(ctx, query, parsed, statusFailed, lastError, time.Now().UTC())
	return err
}

func (r *AdapterStateRepository) EmitDeliveryFailed(ctx context.Context, originalEventID string, maxRetries int) error {
	parsed, err := uuid.Parse(originalEventID)
	if err != nil {
		return err
	}
	payloadBytes, err := json.Marshal(events.DeliveryFailed{
		OriginalEventID: parsed.String(),
		Adapter:         "adapter-matrix",
		Reason:          fmt.Sprintf("Matrix send failed after %d retries", maxRetries),
	})
	if err != nil {
		return err
	}
	query := fmt.Sprintf(`
		INSERT INTO %s (id, event_type, payload, created_at)
		VALUES ($1, $2, $3, $4)
	`, r.outboxTable)
	_, err = r.db.ExecContext(ctx, query, uuid.New(), "DeliveryFailed", payloadBytes, time.Now().UTC())
	return err
}
