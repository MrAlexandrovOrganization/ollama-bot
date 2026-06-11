package bot

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"ollama-bot/internal/ollama"
	"strings"
	"time"

	"github.com/mymmrac/telego"
)

// handleText processes a plain text message: appends it to history and streams an LLM response.
func (b *Bot) handleText(msg *telego.Message) {
	messages := make([]ollama.Message, 0)

	{
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.busy {
			b.send(msg.Chat.ID, "⏳ Подожди, я ещё думаю...")
			return
		}
		b.busy = true
		b.session.addUser(msg.Text)
		messages = b.session.buildMessages()
	}

	ctx := context.Background()
	response, err := b.streamResponse(ctx, msg.Chat.ID, messages)

	{
		b.mu.Lock()
		defer b.mu.Unlock()
		b.busy = false
		if err == nil && response != "" {
			b.session.addAssistant(response)
		} else if err != nil {
			b.session.popLastUser()
		}
	}
}

// handlePhoto downloads a photo, base64-encodes it, and sends it to the current model.
// The message caption (if any) is used as the user's question; defaults to "Что на этом изображении?".
func (b *Bot) handlePhoto(msg *telego.Message) {
	{
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.busy {
			b.mu.Unlock()
			b.send(msg.Chat.ID, "⏳ Подожди, я ещё думаю...")
			return
		}
		b.busy = true
	}

	ctx := context.Background()

	// Take the highest-resolution variant.
	photo := msg.Photo[len(msg.Photo)-1]
	slog.Info("photo received", "file_id", photo.FileID, "size", photo.FileSize)

	data, err := b.downloadFile(ctx, photo.FileID)
	if err != nil {
		slog.Error("download photo", "error", err)
		b.send(msg.Chat.ID, "Не удалось скачать фото: "+err.Error())
		b.mu.Lock()
		b.busy = false
		return
	}

	imageB64 := base64.StdEncoding.EncodeToString(data)
	caption := strings.TrimSpace(msg.Caption)
	if caption == "" {
		caption = "Что на этом изображении?"
	}

	messages := make([]ollama.Message, 0)
	{
		b.mu.Lock()
		defer b.mu.Unlock()
		b.session.addUser(caption, imageB64)
		messages = b.session.buildMessages()
	}

	response, err := b.streamResponse(ctx, msg.Chat.ID, messages)

	{
		b.mu.Lock()
		defer b.mu.Unlock()
		b.busy = false
		if err == nil && response != "" {
			b.session.addAssistant(response)
		} else if err != nil {
			b.session.popLastUser()
		}
	}
}

// handleVoice processes a voice message or video note.
// Requires the Whisper gRPC service to be configured (WHISPER_GRPC_HOST).
func (b *Bot) handleVoice(msg *telego.Message) {
	if b.whisper == nil {
		b.sendHTML(msg.Chat.ID,
			"🎙 Голосовые сообщения требуют настройки сервиса транскрибации.\n\n"+
				"Убедитесь, что в <code>docker-compose.yml</code> указаны:\n"+
				"<code>WHISPER_GRPC_HOST</code> и <code>WHISPER_GRPC_PORT</code>, "+
				"а контейнер подключён к сети <code>whisper-net</code>.",
			nil,
		)
		return
	}

	{
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.busy {
			b.mu.Unlock()
			b.send(msg.Chat.ID, "⏳ Подожди, я ещё думаю...")
			return
		}
		b.busy = true
	}

	ctx := context.Background()

	var fileID string
	switch {
	case msg.Voice != nil:
		fileID = msg.Voice.FileID
	case msg.VideoNote != nil:
		fileID = msg.VideoNote.FileID
	}

	slog.Info("voice received", "file_id", fileID)

	data, err := b.downloadFile(ctx, fileID)
	if err != nil {
		slog.Error("download voice", "error", err)
		b.send(msg.Chat.ID, "Не удалось скачать аудио: "+err.Error())
		b.mu.Lock()
		defer b.mu.Unlock()
		b.busy = false
		return
	}

	b.send(msg.Chat.ID, "🎙 Транскрибирую...")

	text, err := b.transcribeVoice(ctx, data)
	if err != nil {
		slog.Error("transcribe", "error", err)
		b.send(msg.Chat.ID, "Ошибка транскрибации: "+err.Error())
		b.mu.Lock()
		b.busy = false
		b.mu.Unlock()
		return
	}

	slog.Info("transcribed", "chars", len(text))
	b.send(msg.Chat.ID, "📝 "+text)

	messages := make([]ollama.Message, 0)
	{
		b.mu.Lock()
		defer b.mu.Unlock()
		b.session.addUser(text)
		messages = b.session.buildMessages()
	}

	response, err := b.streamResponse(ctx, msg.Chat.ID, messages)

	{
		b.mu.Lock()
		defer b.mu.Unlock()
		b.busy = false
		if err == nil && response != "" {
			b.session.addAssistant(response)
		} else if err != nil {
			b.session.popLastUser()
		}
	}
}

// transcribeVoice submits audio to the Whisper gRPC service and polls for the result.
func (b *Bot) transcribeVoice(ctx context.Context, data []byte) (string, error) {
	jobID, pos, err := b.whisper.Submit(bytes.NewReader(data), "ogg", nil)
	if err != nil {
		return "", fmt.Errorf("submit: %w", err)
	}
	slog.Info("whisper job submitted", "job_id", jobID, "queue_position", pos)

	const pollInterval = 5 * time.Second

	for {
		select {
		case <-ctx.Done():
			_, _ = b.whisper.Cancel(jobID)
			return "", ctx.Err()
		case <-time.After(pollInterval):
		}

		result, err := b.whisper.GetStatus(jobID)
		if err != nil {
			return "", fmt.Errorf("get status: %w", err)
		}
		if result.IsDone() {
			if result.Text == "" {
				return "", fmt.Errorf("whisper вернул пустой текст")
			}
			return strings.TrimSpace(result.Text), nil
		}
		if result.IsFailed() {
			return "", fmt.Errorf("транскрибация завершилась с ошибкой: %s", result.Error)
		}
	}
}
