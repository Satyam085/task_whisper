package config

import (
	"fmt"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

// Config holds all application configuration loaded from environment variables.
type Config struct {
	// Telegram (Optional)
	TelegramToken string
	AllowedUserID int64
	ChatID        int64


	// Gemini
	GeminiAPIKey string

	// Google Tasks List IDs
	GTListPersonal string
	GTListOffice   string
	GTListShopping string
	GTListOthers   string

	// Google Auth
	GoogleCredentialsPath string
	GoogleTokenPath       string

	// App
	PollingInterval int // seconds between polls (default 3)
	Timezone        string
	SummaryTime     string

	// Optional
	SQLitePath string
	LogLevel   string
}

// Load reads configuration from .env file and environment variables.
// Required variables are validated; missing ones cause an error.
func Load() (*Config, error) {
	// Load .env file if it exists (ignore error — env vars may come from systemd)
	_ = godotenv.Load()

	cfg := &Config{}

	// --- Optional Bot Configs ---
	var err error
	cfg.TelegramToken = os.Getenv("TELEGRAM_TOKEN")
	if cfg.TelegramToken != "" {
		cfg.AllowedUserID, err = parseInt64Env("ALLOWED_USER_ID")
		if err != nil {
			return nil, fmt.Errorf("ALLOWED_USER_ID is required when TELEGRAM_TOKEN is set: %w", err)
		}

		cfg.ChatID, err = parseInt64Env("CHAT_ID")
		if err != nil {
			return nil, fmt.Errorf("CHAT_ID is required when TELEGRAM_TOKEN is set: %w", err)
		}
	}


	// --- Required fields ---
	cfg.GeminiAPIKey = os.Getenv("GEMINI_API_KEY")
	if cfg.GeminiAPIKey == "" {
		return nil, fmt.Errorf("GEMINI_API_KEY is required")
	}

	cfg.GTListPersonal = os.Getenv("GTASKS_LIST_PERSONAL")
	if cfg.GTListPersonal == "" {
		return nil, fmt.Errorf("GTASKS_LIST_PERSONAL is required")
	}

	cfg.GTListOffice = os.Getenv("GTASKS_LIST_OFFICE")
	if cfg.GTListOffice == "" {
		return nil, fmt.Errorf("GTASKS_LIST_OFFICE is required")
	}

	cfg.GTListShopping = os.Getenv("GTASKS_LIST_SHOPPING")
	if cfg.GTListShopping == "" {
		return nil, fmt.Errorf("GTASKS_LIST_SHOPPING is required")
	}

	cfg.GTListOthers = os.Getenv("GTASKS_LIST_OTHERS")
	if cfg.GTListOthers == "" {
		return nil, fmt.Errorf("GTASKS_LIST_OTHERS is required")
	}

	// --- Optional with defaults ---
	cfg.GoogleCredentialsPath = getEnvDefault("GOOGLE_CREDENTIALS_PATH", "./credentials.json")
	cfg.GoogleTokenPath = getEnvDefault("GOOGLE_TOKEN_PATH", "./token.json")
	cfg.PollingInterval = 3
	if val := os.Getenv("POLLING_INTERVAL"); val != "" {
		if n, err := strconv.Atoi(val); err == nil && n > 0 {
			cfg.PollingInterval = n
		}
	}
	cfg.Timezone = getEnvDefault("TIMEZONE", "Asia/Kolkata")
	cfg.SummaryTime = getEnvDefault("SUMMARY_TIME", "08:00")
	cfg.SQLitePath = getEnvDefault("SQLITE_PATH", "./tasks.db")
	cfg.LogLevel = getEnvDefault("LOG_LEVEL", "info")

	return cfg, nil
}

func parseInt64Env(key string) (int64, error) {
	val := os.Getenv(key)
	if val == "" {
		return 0, fmt.Errorf("%s is required", key)
	}
	return strconv.ParseInt(val, 10, 64)
}

func getEnvDefault(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}
