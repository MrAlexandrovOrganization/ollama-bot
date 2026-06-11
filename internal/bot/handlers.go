package bot

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/mymmrac/telego"
)

// handleText processes a plain text message: appends it to history and streams an LLM response.
func (b *Bot) handleText(msg *telego.Message) {
	if !b.tryAcquire() {
		b.send(msg.Chat.ID, "⏳ Подожди, я ещё думаю...")
		return
	}
	messages, model := b.startMessage(msg.Text)
	response, err := b.streamResponse(context.Background(), msg.Chat.ID, model, messages)
	b.finish(response, err)
}

// handlePhoto downloads a photo, base64-encodes it, and sends it to the current model.
// The message caption (if any) is used as the user's question; defaults to "Что на этом изображении?".
func (b *Bot) handlePhoto(msg *telego.Message) {
	if !b.tryAcquire() {
		b.send(msg.Chat.ID, "⏳ Подожди, я ещё думаю...")
		return
	}

	ctx := context.Background()

	// Take the highest-resolution variant.
	photo := msg.Photo[len(msg.Photo)-1]
	slog.Info("photo received", "file_id", photo.FileID, "size", photo.FileSize)

	data, err := b.downloadFile(ctx, photo.FileID)
	if err != nil {
		slog.Error("download photo", "error", err)
		b.release()
		b.send(msg.Chat.ID, "Не удалось скачать фото: "+err.Error())
		return
	}

	imageB64 := base64.StdEncoding.EncodeToString(data)
	caption := strings.TrimSpace(msg.Caption)
	if caption == "" {
		caption = "Что на этом изображении?"
	}

	messages, model := b.startMessage(caption, imageB64)
	response, err := b.streamResponse(ctx, msg.Chat.ID, model, messages)
	b.finish(response, err)
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

	if !b.tryAcquire() {
		b.send(msg.Chat.ID, "⏳ Подожди, я ещё думаю...")
		return
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
		b.release()
		b.send(msg.Chat.ID, "Не удалось скачать аудио: "+err.Error())
		return
	}

	b.send(msg.Chat.ID, "🎙 Транскрибирую...")

	text, err := b.transcribeVoice(ctx, data)
	if err != nil {
		slog.Error("transcribe", "error", err)
		b.release()
		b.send(msg.Chat.ID, "Ошибка транскрибации: "+err.Error())
		return
	}

	slog.Info("transcribed", "chars", len(text))
	b.send(msg.Chat.ID, "📝 "+text)

	messages, model := b.startMessage(text)
	response, err := b.streamResponse(ctx, msg.Chat.ID, model, messages)
	b.finish(response, err)
}

// transcribeVoice submits audio to the Whisper gRPC service and polls for the result.
func (b *Bot) transcribeVoice(ctx context.Context, data []byte) (string, error) {
	jobID, pos, err := b.whisper.Submit(bytes.NewReader(data), "ogg", nil)
	if err != nil {
		return "", fmt.Errorf("submit: %w", err)
	}
	slog.Info("whisper job submitted", "job_id", jobID, "queue_position", pos)

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			_, _ = b.whisper.Cancel(jobID)
			return "", ctx.Err()
		case <-ticker.C:
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
