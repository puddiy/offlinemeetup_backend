// Command adminctl создаёт учётки администраторов.
//
// Первого админа взять неоткуда: панель закрыта логином, а логиниться некем.
// Поэтому — отдельная утилита, а не seed из переменных окружения: секрет,
// живущий в env прода месяцами, хуже секрета, который ввели один раз.
//
//	go run ./cmd/adminctl -email=admin@meetuper.site -role=admin
//
// Пароль читается со stdin (ввод ЭХОится — это осознанная простота ради
// нулевых зависимостей; для скрытого ввода передавай пароль через пайп:
// `printf '%s' "$PASS" | go run ./cmd/adminctl -email=...`).
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/puddingtonnn/offlinemeetup_backend/internal/config"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/db"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/repo"
	_ "github.com/uptrace/bun/driver/pgdriver"
	"golang.org/x/crypto/bcrypt"
)

// bcryptCost совпадает с internal/service/auth_password.go: хэши админов и
// пользователей обязаны иметь одну стойкость.
const bcryptCost = 12

// minPasswordLen — тот же нижний порог, что у пользовательских паролей
// (см. internal/dto/auth.go). Верхний предел bcrypt — 72 БАЙТА, не символа.
const (
	minPasswordLen = 8
	maxPasswordLen = 72
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "adminctl:", err)
		os.Exit(1)
	}
}

func run() error {
	email := flag.String("email", "", "email администратора")
	role := flag.String("role", "admin", "роль: admin или moderator")
	flag.Parse()

	if strings.TrimSpace(*email) == "" {
		return errors.New("-email обязателен")
	}
	adminRole := domain.AdminRole(*role)
	if !adminRole.Valid() {
		return fmt.Errorf("неизвестная роль %q (допустимы: admin, moderator)", *role)
	}

	password, err := readPassword()
	if err != nil {
		return err
	}
	if len(password) < minPasswordLen {
		return fmt.Errorf("пароль короче %d символов", minPasswordLen)
	}
	if len(password) > maxPasswordLen {
		return fmt.Errorf("пароль длиннее %d байт — bcrypt молча обрежет остаток", maxPasswordLen)
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("загрузка конфига: %w", err)
	}

	database, err := db.New(cfg.DBDSN)
	if err != nil {
		return fmt.Errorf("подключение к БД: %w", err)
	}
	defer func() { _ = database.Close() }()

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return fmt.Errorf("хэширование пароля: %w", err)
	}

	adminRepo := repo.NewAdminRepo(database)
	admin := &domain.AdminUser{
		Email:        *email,
		PasswordHash: string(hash),
		Role:         adminRole,
		IsActive:     true,
	}

	if err := adminRepo.Create(context.Background(), admin); err != nil {
		if errors.Is(err, repo.ErrAdminEmailTaken) {
			return fmt.Errorf("админ с email %s уже существует", *email)
		}
		return fmt.Errorf("создание админа: %w", err)
	}

	fmt.Printf("Создан администратор id=%d email=%s role=%s\n", admin.ID, admin.Email, admin.Role)
	return nil
}

// readPassword читает пароль со stdin — одной строкой либо целиком из пайпа.
func readPassword() (string, error) {
	stat, err := os.Stdin.Stat()
	if err != nil {
		return "", fmt.Errorf("чтение stdin: %w", err)
	}

	// Пайп: читаем всё и обрезаем перевод строки.
	if stat.Mode()&os.ModeCharDevice == 0 {
		raw, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("чтение пароля: %w", err)
		}
		return strings.TrimRight(string(raw), "\r\n"), nil
	}

	fmt.Print("Пароль (ввод виден на экране): ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("чтение пароля: %w", err)
	}
	return strings.TrimRight(line, "\r\n"), nil
}
