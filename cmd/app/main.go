package main

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/puddingtonnn/offlinemeetup_backend/migrations"

	"github.com/pressly/goose/v3"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/app"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/config"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/db"
	_ "github.com/uptrace/bun/driver/pgdriver"
)

// @title           Offline Meetup API
// @version         1.0
// @description     API Server for Mobile Application "Meetuper"

// @BasePath        /

// @securityDefinitions.apikey  BearerAuth
// @in                          header
// @name                        Authorization
// @description                 Введите токен в формате: Bearer <your-token>
func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := config.Load()
	if err != nil {
		logger.Error("Failed to load config", slog.String("err", err.Error()))
		os.Exit(1)
	}

	rawDB, err := sql.Open("pg", cfg.DBDSN)
	if err != nil {
		logger.Error("Failed to open database for migrations", slog.String("err", err.Error()))
		os.Exit(1)
	}

	goose.SetBaseFS(migrations.EmbedFS)
	if err := goose.SetDialect("postgres"); err != nil {
		logger.Error("Failed to set postgres dialect", slog.String("err", err.Error()))
		os.Exit(1)
	}

	if err := goose.Up(rawDB, "."); err != nil {
		logger.Error("Migration failed", slog.String("err", err.Error()))
		os.Exit(1)
	}

	// Пул для goose нужен только на время миграций: закрываем сразу, а не
	// держим второй пул соединений до остановки процесса.
	if err := rawDB.Close(); err != nil {
		logger.Warn("Closing migration database", slog.String("err", err.Error()))
	}

	database, err := db.New(cfg.DBDSN)
	if err != nil {
		logger.Error("Failed to connect to database", slog.String("err", err.Error()))
		os.Exit(1)
	}
	defer func() {
		if err := database.Close(); err != nil {
			logger.Error("Closing database", slog.String("err", err.Error()))
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	application := app.New(logger, cfg, database)

	if err := application.Run(ctx); err != nil {
		logger.Error("Failed to run", slog.String("err", err.Error()))
		os.Exit(1)
	}

	logger.Info("Application stopped gracefully")
}
