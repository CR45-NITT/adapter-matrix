package consumer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"adapter-matrix/internal/matrix"
	"adapter-matrix/internal/repository"
)

type OutboxConsumer struct {
	db           *sql.DB
	repo         *repository.AdapterStateRepository
	matrix       *matrix.Client
	outboxTables []string
	pollInterval time.Duration
	maxRetries   int
	batchSize    int
	logger       *log.Logger

	stopOnce sync.Once
	stopCh   chan struct{}
	wg       sync.WaitGroup
}

type MessagePayload struct {
	RoomID string `json:"room_id"`
	Body   string `json:"body"`
	Format string `json:"format"`
}

type timetableSlotPayload struct {
	SlotIndex  int    `json:"slot_index"`
	CourseCode string `json:"course_code"`
	StartTime  string `json:"start_time"`
	EndTime    string `json:"end_time"`
	Venue      string `json:"venue"`
	Status     string `json:"status"`
}

type timetableAnnouncedPayload struct {
	ClassID      string                 `json:"class_id"`
	Date         string                 `json:"date"`
	MatrixRoomID string                 `json:"matrix_room_id"`
	Template     string                 `json:"template"`
	Slots        []timetableSlotPayload `json:"slots"`
}

type timetableUpdatedPayload struct {
	ClassID        string                 `json:"class_id"`
	Date           string                 `json:"date"`
	MatrixRoomID   string                 `json:"matrix_room_id"`
	UpdateTemplate string                 `json:"update_template"`
	Slots          []timetableSlotPayload `json:"slots"`
	UpdatedBy      string                 `json:"updated_by"`
}

func NewOutboxConsumer(
	db *sql.DB,
	repo *repository.AdapterStateRepository,
	matrixClient *matrix.Client,
	outboxTables []string,
	pollInterval time.Duration,
	maxRetries int,
	batchSize int,
	logger *log.Logger,
) *OutboxConsumer {
	return &OutboxConsumer{
		db:           db,
		repo:         repo,
		matrix:       matrixClient,
		outboxTables: outboxTables,
		pollInterval: pollInterval,
		maxRetries:   maxRetries,
		batchSize:    batchSize,
		logger:       logger,
		stopCh:       make(chan struct{}),
	}
}

func (c *OutboxConsumer) Start(ctx context.Context) error {
	c.wg.Add(1)
	go c.loop(ctx)
	return nil
}

func (c *OutboxConsumer) Stop(ctx context.Context) error {
	c.stopOnce.Do(func() { close(c.stopCh) })
	ch := make(chan struct{})
	go func() {
		defer close(ch)
		c.wg.Wait()
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-ch:
		return nil
	}
}

func (c *OutboxConsumer) loop(ctx context.Context) {
	defer c.wg.Done()
	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()

	for {
		if err := c.pollOnce(ctx); err != nil {
			c.logger.Printf("poll error: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-c.stopCh:
			return
		case <-ticker.C:
		}
	}
}

func (c *OutboxConsumer) pollOnce(ctx context.Context) error {
	for _, table := range c.outboxTables {
		if err := c.pollTable(ctx, table); err != nil {
			return err
		}
	}
	return nil
}

func (c *OutboxConsumer) pollTable(ctx context.Context, table string) error {
	query := fmt.Sprintf("SELECT id, event_type, payload FROM %s ORDER BY id LIMIT $1", table)
	rows, err := c.db.QueryContext(ctx, query, c.batchSize)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var eventID string
		var eventType string
		var payloadBytes []byte
		if err := rows.Scan(&eventID, &eventType, &payloadBytes); err != nil {
			return err
		}
		if err := c.processEvent(ctx, table, eventID, eventType, payloadBytes); err != nil {
			c.logger.Printf("event processing error: %v", err)
		}
	}

	return rows.Err()
}

func (c *OutboxConsumer) processEvent(ctx context.Context, table, eventID, eventType string, payloadBytes []byte) error {
	payload, err := decodeEventPayload(eventType, payloadBytes)
	if err != nil {
		return c.handleFailure(ctx, eventID, fmt.Errorf("payload decode: %w", err))
	}
	payload.Format = strings.ToLower(strings.TrimSpace(payload.Format))
	if payload.RoomID == "" || payload.Body == "" || payload.Format == "" {
		return c.handleFailure(ctx, eventID, errors.New("payload missing required fields"))
	}
	if payload.Format != "plain" && payload.Format != "markdown" && payload.Format != "html" {
		return c.handleFailure(ctx, eventID, errors.New("unsupported payload format"))
	}

	attempts, claimed, err := c.repo.ClaimEvent(ctx, eventID)
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}

	if err := c.matrix.SendMessage(ctx, payload.RoomID, payload.Body, payload.Format); err != nil {
		if attempts >= c.maxRetries {
			return c.handlePermanentFailure(ctx, eventID, err)
		}
		return c.repo.MarkRetry(ctx, eventID, err.Error())
	}

	return c.repo.MarkSent(ctx, eventID)
}

func decodeEventPayload(eventType string, payloadBytes []byte) (MessagePayload, error) {
	var messagePayload MessagePayload
	if err := json.Unmarshal(payloadBytes, &messagePayload); err == nil {
		if strings.TrimSpace(messagePayload.RoomID) != "" || strings.TrimSpace(messagePayload.Body) != "" {
			return messagePayload, nil
		}
	}

	switch strings.TrimSpace(eventType) {
	case "DailyTimetableAnnounced":
		var payload timetableAnnouncedPayload
		if err := json.Unmarshal(payloadBytes, &payload); err != nil {
			return MessagePayload{}, err
		}
		if strings.TrimSpace(payload.MatrixRoomID) == "" {
			return MessagePayload{}, errors.New("daily announcement missing matrix_room_id")
		}
		return MessagePayload{
			RoomID: payload.MatrixRoomID,
			Body:   renderTimetableMessage(payload.Template, payload.Date, payload.Slots),
			Format: "markdown",
		}, nil
	case "TimetableUpdated":
		var payload timetableUpdatedPayload
		if err := json.Unmarshal(payloadBytes, &payload); err != nil {
			return MessagePayload{}, err
		}
		if strings.TrimSpace(payload.MatrixRoomID) == "" {
			return MessagePayload{}, errors.New("timetable update missing matrix_room_id")
		}
		return MessagePayload{
			RoomID: payload.MatrixRoomID,
			Body:   renderTimetableMessage(payload.UpdateTemplate, payload.Date, payload.Slots),
			Format: "markdown",
		}, nil
	default:
		return MessagePayload{}, errors.New("unsupported event payload")
	}
}

func renderTimetableMessage(templateText, date string, slots []timetableSlotPayload) string {
	title := strings.TrimSpace(templateText)
	if title == "" {
		title = "Timetable update"
	}

	lines := make([]string, 0, len(slots)+2)
	lines = append(lines, title)
	if strings.TrimSpace(date) != "" {
		lines = append(lines, "Date: "+date)
	}

	for _, slot := range slots {
		line := fmt.Sprintf("%d. %s (%s-%s) @ %s [%s]", slot.SlotIndex, safeText(slot.CourseCode), safeText(slot.StartTime), safeText(slot.EndTime), safeText(slot.Venue), safeText(slot.Status))
		lines = append(lines, line)
	}

	return strings.Join(lines, "\n")
}

func safeText(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "-"
	}
	return trimmed
}

func (c *OutboxConsumer) handleFailure(ctx context.Context, eventID string, err error) error {
	attempts, claimed, claimErr := c.repo.ClaimEvent(ctx, eventID)
	if claimErr != nil {
		return claimErr
	}
	if !claimed {
		return nil
	}
	if attempts >= c.maxRetries {
		return c.handlePermanentFailure(ctx, eventID, err)
	}
	return c.repo.MarkRetry(ctx, eventID, err.Error())
}

func (c *OutboxConsumer) handlePermanentFailure(ctx context.Context, eventID string, err error) error {
	if updateErr := c.repo.MarkFailed(ctx, eventID, err.Error()); updateErr != nil {
		return updateErr
	}
	return c.repo.EmitDeliveryFailed(ctx, eventID, c.maxRetries)
}
