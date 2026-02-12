package app

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"time"

	"adapter-matrix/internal/consumer"
	"adapter-matrix/internal/matrix"
	"adapter-matrix/internal/repository"
	adaptermigrations "adapter-matrix/migrations"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type Config struct {
	DatabaseURL     string
	HomeserverURL   string
	MatrixUserID    string
	AccessToken     string
	PollInterval    time.Duration
	MaxRetries      int
	AllowedRoomIDs  []string
	OutboxTables    []string
	AdapterOutbox   string
	OutboxBatchSize int
}

type App struct {
	cfg      Config
	logger   *log.Logger
	db       *sql.DB
	matrix   *matrix.Client
	consumer *consumer.OutboxConsumer
	syncStop func()
}

func New(cfg Config, logger *log.Logger) (*App, error) {
	if logger == nil {
		return nil, errors.New("logger is required")
	}
	for _, table := range cfg.OutboxTables {
		if !repository.IsValidTableName(table) {
			return nil, errors.New("outbox table name contains invalid characters")
		}
	}

	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)

	if err := db.Ping(); err != nil {
		return nil, err
	}

	if err := adaptermigrations.Up(db); err != nil {
		return nil, err
	}

	matrixClient, err := matrix.NewClient(cfg.HomeserverURL, cfg.MatrixUserID, cfg.AccessToken, cfg.AllowedRoomIDs, logger)
	if err != nil {
		return nil, err
	}

	repo, err := repository.NewAdapterStateRepository(db, cfg.AdapterOutbox)
	if err != nil {
		return nil, err
	}

	consumer := consumer.NewOutboxConsumer(
		db,
		repo,
		matrixClient,
		cfg.OutboxTables,
		cfg.PollInterval,
		cfg.MaxRetries,
		cfg.OutboxBatchSize,
		logger,
	)

	return &App{
		cfg:      cfg,
		logger:   logger,
		db:       db,
		matrix:   matrixClient,
		consumer: consumer,
	}, nil
}

func (a *App) Start(ctx context.Context) error {
	if err := a.consumer.Start(ctx); err != nil {
		return err
	}

	syncCtx, cancel := context.WithCancel(ctx)
	a.syncStop = cancel
	go func() {
		if err := a.matrix.StartSync(syncCtx); err != nil && !errors.Is(err, context.Canceled) {
			a.logger.Printf("matrix sync stopped: %v", err)
		}
	}()

	return nil
}

func (a *App) Stop(ctx context.Context) error {
	if a.syncStop != nil {
		a.syncStop()
	}

	if err := a.consumer.Stop(ctx); err != nil {
		a.logger.Printf("consumer stop error: %v", err)
	}

	if err := a.db.Close(); err != nil {
		return err
	}
	return nil
}
