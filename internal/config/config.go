package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config holds all bot configuration loaded from environment variables.
type Config struct {
	BotToken        string
	RootID          int64
	OllamaHost      string
	OllamaPort      string
	DefaultModel    string
	WhisperGRPCHost string
	WhisperGRPCPort string
}

func Load() (*Config, error) {
	token := os.Getenv("BOT_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("BOT_TOKEN is required")
	}

	rootIDStr := os.Getenv("ROOT_ID")
	if rootIDStr == "" {
		return nil, fmt.Errorf("ROOT_ID is required")
	}
	rootID, err := strconv.ParseInt(rootIDStr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("ROOT_ID must be a number: %w", err)
	}

	return &Config{
		BotToken:        token,
		RootID:          rootID,
		OllamaHost:      getEnv("OLLAMA_HOST", "localhost"),
		OllamaPort:      getEnv("OLLAMA_PORT", "11434"),
		DefaultModel:    getEnv("DEFAULT_MODEL", "qwen2.5:7b"),
		WhisperGRPCHost: getEnv("WHISPER_GRPC_HOST", ""),
		WhisperGRPCPort: getEnv("WHISPER_GRPC_PORT", "50053"),
	}, nil
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
