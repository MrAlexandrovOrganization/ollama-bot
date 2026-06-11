package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"ollama-bot/internal/bot"
	"ollama-bot/internal/config"
	"ollama-bot/internal/ollama"
	"ollama-bot/internal/whisper"

	"github.com/mymmrac/telego"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "error", err)
		os.Exit(1)
	}

	api, err := telego.NewBot(cfg.BotToken)
	if err != nil {
		slog.Error("bot init", "error", err)
		os.Exit(1)
	}

	ollamaClient := ollama.New(cfg.OllamaHost, cfg.OllamaPort)

	var whisperClient *whisper.Client
	if cfg.WhisperGRPCHost != "" {
		whisperClient, err = whisper.NewClient(cfg.WhisperGRPCHost, cfg.WhisperGRPCPort)
		if err != nil {
			slog.Error("whisper client", "error", err)
			os.Exit(1)
		}
		defer whisperClient.Close()
		slog.Info("whisper configured", "host", cfg.WhisperGRPCHost, "port", cfg.WhisperGRPCPort)
	}

	b := bot.New(api, ollamaClient, whisperClient, cfg)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	slog.Info("starting ollama bot")
	b.Run(ctx)
	slog.Info("stopped")
}
