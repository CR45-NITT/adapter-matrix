package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"adapter-matrix/internal/app"
)

func main() {
	logger := log.New(os.Stdout, "", log.LstdFlags|log.LUTC)

	cfg, err := loadConfig()
	if err != nil {
		logger.Fatalf("config error: %v", err)
	}

	debugEnabled := strings.EqualFold(strings.TrimSpace(getEnv("LOG_LEVEL", "info")), "debug")
	debugf := func(format string, args ...any) {
		if debugEnabled {
			logger.Printf("[DEBUG] "+format, args...)
		}
	}
	debugf("config loaded: homeserver_url=%s matrix_user_id=%s outbox_tables=%v adapter_outbox=%s poll_interval=%s max_retries=%d batch_size=%d allowed_room_ids=%v",
		cfg.HomeserverURL,
		cfg.MatrixUserID,
		cfg.OutboxTables,
		cfg.AdapterOutbox,
		cfg.PollInterval,
		cfg.MaxRetries,
		cfg.OutboxBatchSize,
		cfg.AllowedRoomIDs,
	)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	application, err := app.New(cfg, logger)
	if err != nil {
		logger.Fatalf("app init error: %v", err)
	}

	if err := application.Start(ctx); err != nil {
		logger.Fatalf("app start error: %v", err)
	}
	debugf("application started")

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := application.Stop(shutdownCtx); err != nil {
		logger.Printf("shutdown error: %v", err)
	}
	debugf("application stopped")
}

func loadConfig() (app.Config, error) {
	var cfg app.Config

	cfg.DatabaseURL = strings.TrimSpace(os.Getenv("DATABASE_URL"))
	cfg.HomeserverURL = strings.TrimSpace(os.Getenv("MATRIX_HOMESERVER_URL"))
	cfg.MatrixUserID = strings.TrimSpace(os.Getenv("MATRIX_USER_ID"))
	cfg.AccessToken = strings.TrimSpace(os.Getenv("MATRIX_ACCESS_TOKEN"))
	cfg.AdapterOutbox = strings.TrimSpace(getEnv("ADAPTER_OUTBOX_TABLE", "adapter_outbox"))

	pollIntervalStr := strings.TrimSpace(getEnv("POLL_INTERVAL", "5s"))
	pollInterval, err := time.ParseDuration(pollIntervalStr)
	if err != nil {
		return cfg, err
	}
	cfg.PollInterval = pollInterval

	maxRetriesStr := strings.TrimSpace(getEnv("MAX_RETRIES", "5"))
	maxRetries, err := strconv.Atoi(maxRetriesStr)
	if err != nil {
		return cfg, err
	}
	cfg.MaxRetries = maxRetries

	batchSizeStr := strings.TrimSpace(getEnv("OUTBOX_BATCH_SIZE", "100"))
	batchSize, err := strconv.Atoi(batchSizeStr)
	if err != nil {
		return cfg, err
	}
	cfg.OutboxBatchSize = batchSize

	allowedRoomsStr := strings.TrimSpace(getEnv("ALLOWED_ROOM_IDS", ""))
	if allowedRoomsStr != "" {
		cfg.AllowedRoomIDs = splitCSV(allowedRoomsStr)
	}

	outboxTablesStr := strings.TrimSpace(os.Getenv("OUTBOX_TABLES"))
	if outboxTablesStr != "" {
		cfg.OutboxTables = splitCSV(outboxTablesStr)
	}

	if cfg.DatabaseURL == "" || cfg.HomeserverURL == "" || cfg.AccessToken == "" {
		return cfg, errMissingEnv
	}
	if cfg.MatrixUserID == "" {
		return cfg, errMissingMatrixUserID
	}
	if !isValidMatrixUserID(cfg.MatrixUserID) {
		return cfg, errInvalidMatrixUserID
	}
	if len(cfg.OutboxTables) == 0 {
		return cfg, errMissingOutboxTables
	}
	if cfg.MaxRetries < 1 {
		return cfg, errInvalidMaxRetries
	}
	if cfg.OutboxBatchSize < 1 {
		return cfg, errInvalidBatchSize
	}

	return cfg, nil
}

func isValidMatrixUserID(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	if !strings.HasPrefix(trimmed, "@") {
		return false
	}
	return strings.Contains(trimmed, ":")
}

func splitCSV(input string) []string {
	parts := strings.Split(input, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

var (
	errMissingEnv          = &configError{"required env vars missing: DATABASE_URL, MATRIX_HOMESERVER_URL, MATRIX_ACCESS_TOKEN"}
	errMissingMatrixUserID = &configError{"MATRIX_USER_ID is required"}
	errInvalidMatrixUserID = &configError{"MATRIX_USER_ID must look like @user:domain"}
	errMissingOutboxTables = &configError{"OUTBOX_TABLES is required"}
	errInvalidMaxRetries   = &configError{"MAX_RETRIES must be >= 1"}
	errInvalidBatchSize    = &configError{"OUTBOX_BATCH_SIZE must be >= 1"}
)

type configError struct {
	message string
}

func (e *configError) Error() string {
	return e.message
}
