package bot

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"ollama-bot/internal/config"
	"ollama-bot/internal/ollama"
	"ollama-bot/internal/whisper"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"
)

// maxMessageLen is the safe Telegram message length (actual limit is 4096).
const maxMessageLen = 4000

// Bot is the main application struct.
type Bot struct {
	api     *telego.Bot
	ollama  *ollama.Client
	whisper *whisper.Client // nil if Whisper is not configured
	cfg     *config.Config
	mu      sync.Mutex
	session *Session
	busy    bool // true while an LLM request is in flight
}

// New creates a new Bot.
func New(api *telego.Bot, ollamaClient *ollama.Client, whisperClient *whisper.Client, cfg *config.Config) *Bot {
	return &Bot{
		api:     api,
		ollama:  ollamaClient,
		whisper: whisperClient,
		cfg:     cfg,
		session: newSession(cfg.DefaultModel),
	}
}

// Run starts long polling and processes updates until ctx is cancelled.
func (b *Bot) Run(ctx context.Context) {
	updates, err := b.api.UpdatesViaLongPolling(ctx, nil)
	if err != nil {
		slog.Error("long polling", "error", err)
		return
	}
	slog.Info("bot started", "username", b.api.Username())
	for update := range updates {
		go b.handleUpdate(update)
	}
}

// ── Streaming ─────────────────────────────────────────────────────────────────

// streamResponse sends a "thinking" placeholder and streams the LLM response into it.
// Returns the full response text.
func (b *Bot) streamResponse(ctx context.Context, chatID int64, messages []ollama.Message) (string, error) {
	_ = b.api.SendChatAction(ctx, &telego.SendChatActionParams{
		ChatID: telego.ChatID{ID: chatID},
		Action: "typing",
	})

	placeholder, err := b.api.SendMessage(ctx, tu.Message(tu.ID(chatID), "💭"))
	if err != nil {
		return "", fmt.Errorf("send placeholder: %w", err)
	}

	type result struct {
		text string
		err  error
	}

	doneCh := make(chan result, 1)
	var mu sync.Mutex
	var partial string

	model := b.session.Model
	go func() {
		full, err := b.ollama.ChatStream(ctx, model, messages, func(chunk string) {
			mu.Lock()
			partial += chunk
			mu.Unlock()
		})
		doneCh <- result{full, err}
	}()

	ticker := time.NewTicker(1200 * time.Millisecond)
	defer ticker.Stop()
	var lastText string

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case r := <-doneCh:
			if r.err != nil {
				b.editMessage(ctx, chatID, placeholder.MessageID, "❌ "+r.err.Error())
				return "", r.err
			}
			if r.text != lastText {
				b.sendFinalResponse(ctx, chatID, placeholder.MessageID, r.text)
			}
			return r.text, nil
		case <-ticker.C:
			mu.Lock()
			curr := partial
			mu.Unlock()
			if curr != lastText && curr != "" {
				b.editMessage(ctx, chatID, placeholder.MessageID, truncate(curr)+"▌")
				lastText = curr
			}
		}
	}
}

// sendFinalResponse edits the placeholder with the full response,
// or sends the response as a .txt document if it exceeds the Telegram size limit.
func (b *Bot) sendFinalResponse(ctx context.Context, chatID int64, msgID int, text string) {
	if len(text) <= maxMessageLen {
		b.editMessage(ctx, chatID, msgID, text)
		return
	}
	_ = b.api.DeleteMessage(ctx, &telego.DeleteMessageParams{
		ChatID:    telego.ChatID{ID: chatID},
		MessageID: msgID,
	})
	_, err := b.api.SendDocument(ctx,
		tu.Document(tu.ID(chatID), tu.FileFromBytes([]byte(text), "response.txt")),
	)
	if err != nil {
		slog.Error("sendDocument", "error", err)
	}
}

// ── Send helpers ──────────────────────────────────────────────────────────────

func (b *Bot) send(chatID int64, text string) {
	if _, err := b.api.SendMessage(context.Background(), tu.Message(tu.ID(chatID), text)); err != nil {
		slog.Error("send", "error", err)
	}
}

func (b *Bot) sendHTML(chatID int64, text string, kb *telego.InlineKeyboardMarkup) {
	params := tu.Message(tu.ID(chatID), text).WithParseMode(telego.ModeHTML)
	if kb != nil {
		params = params.WithReplyMarkup(kb)
	}
	if _, err := b.api.SendMessage(context.Background(), params); err != nil {
		slog.Error("sendHTML", "error", err)
	}
}

func (b *Bot) editMessage(ctx context.Context, chatID int64, msgID int, text string) {
	params := tu.EditMessageText(tu.ID(chatID), msgID, truncate(text))
	if _, err := b.api.EditMessageText(ctx, params); err != nil {
		slog.Debug("editMessage", "error", err)
	}
}

// ── File download ─────────────────────────────────────────────────────────────

func (b *Bot) downloadFile(ctx context.Context, fileID string) ([]byte, error) {
	file, err := b.api.GetFile(ctx, &telego.GetFileParams{FileID: fileID})
	if err != nil {
		return nil, fmt.Errorf("get file info: %w", err)
	}
	url := b.api.FileDownloadURL(file.FilePath)
	resp, err := http.Get(url) //nolint:noctx
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	return data, nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func truncate(s string) string {
	if len(s) <= maxMessageLen {
		return s
	}
	return s[:maxMessageLen-3] + "..."
}
