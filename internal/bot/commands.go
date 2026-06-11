package bot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"
)

// handleCommand dispatches bot commands.
func (b *Bot) handleCommand(cmd string, msg *telego.Message) {
	switch cmd {
	case "start":
		b.cmdStart(msg)
	case "help":
		b.cmdHelp(msg)
	case "reset":
		b.cmdReset(msg)
	case "model":
		b.cmdModel(msg)
	case "models":
		b.cmdModels(msg)
	case "system":
		b.cmdSystem(msg)
	case "session":
		b.cmdSession(msg)
	default:
		b.send(msg.Chat.ID, "Неизвестная команда. /help — список команд.")
	}
}

func (b *Bot) cmdStart(msg *telego.Message) {
	b.sendHTML(msg.Chat.ID,
		"Привет! Я <b>Ollama-бот</b> 🤖\n\n"+
			"Я перенаправляю твои сообщения в локальную LLM и стримлю ответы прямо в Telegram.\n\n"+
			"Отправь /help для списка команд.",
		nil,
	)
}

func (b *Bot) cmdHelp(msg *telego.Message) {
	b.sendHTML(msg.Chat.ID, `<b>Команды</b>

/start — приветствие
/help — этот список
/reset — очистить историю диалога
/model — выбрать модель (inline-клавиатура)
/models — список доступных моделей
/system <i>[промпт]</i> — задать системный промпт (/system без текста — очистить)
/session — информация о текущей сессии

<b>Медиа</b>

📝 <b>Текст</b> — передаётся в LLM, история сохраняется
🖼 <b>Фото</b> — отправляется мультимодальной модели вместе с подписью
🎙 <b>Голос / видео-кружок</b> — транскрибируется (требует <code>WHISPER_URL</code>), затем передаётся в LLM`, nil)
}

func (b *Bot) cmdReset(msg *telego.Message) {
	b.mu.Lock()
	count := len(b.session.Messages)
	b.session.reset()
	b.mu.Unlock()
	b.send(msg.Chat.ID, fmt.Sprintf("История очищена (%d сообщений удалено).", count))
}

func (b *Bot) cmdModels(msg *telego.Message) {
	ctx := context.Background()
	models, err := b.ollama.ListModels(ctx)
	if err != nil {
		slog.Error("list models", "error", err)
		b.send(msg.Chat.ID, "Не удалось получить список моделей: "+err.Error())
		return
	}
	if len(models) == 0 {
		b.send(msg.Chat.ID, "Нет доступных моделей.")
		return
	}

	b.mu.Lock()
	current := b.session.Model
	b.mu.Unlock()

	var sb strings.Builder
	sb.WriteString("<b>Доступные модели:</b>\n")
	for _, m := range models {
		if m == current {
			sb.WriteString("• " + escapeHTML(m) + " ✓\n")
		} else {
			sb.WriteString("• " + escapeHTML(m) + "\n")
		}
	}
	b.sendHTML(msg.Chat.ID, sb.String(), nil)
}

func (b *Bot) cmdModel(msg *telego.Message) {
	ctx := context.Background()
	models, err := b.ollama.ListModels(ctx)
	if err != nil {
		slog.Error("list models", "error", err)
		b.send(msg.Chat.ID, "Не удалось получить список моделей: "+err.Error())
		return
	}
	if len(models) == 0 {
		b.send(msg.Chat.ID, "Нет доступных моделей.")
		return
	}

	b.mu.Lock()
	current := b.session.Model
	b.mu.Unlock()

	kb := buildModelKeyboard(models, current)
	b.sendHTML(msg.Chat.ID, "Выберите модель:", &kb)
}

func (b *Bot) cmdSystem(msg *telego.Message) {
	_, args, _ := tu.ParseCommand(msg.Text)
	prompt := strings.TrimSpace(args)

	b.mu.Lock()
	defer b.mu.Unlock()

	if prompt == "" {
		b.session.SystemPrompt = ""
		go b.send(msg.Chat.ID, "Системный промпт очищен.")
		return
	}
	b.session.SystemPrompt = prompt
	go b.send(msg.Chat.ID, "✅ Системный промпт установлен.")
}

func (b *Bot) cmdSession(msg *telego.Message) {
	b.mu.Lock()
	model := b.session.Model
	system := b.session.SystemPrompt
	msgCount := len(b.session.Messages)
	created := b.session.CreatedAt
	b.mu.Unlock()

	sysText := "<i>(не задан)</i>"
	if system != "" {
		sysText = "<blockquote>" + escapeHTML(truncate(system)) + "</blockquote>"
	}

	text := fmt.Sprintf(
		"<b>Сессия</b>\n\n"+
			"🤖 <b>Модель:</b> %s\n"+
			"💬 <b>Сообщений:</b> %d\n"+
			"🕐 <b>Начата:</b> %s\n\n"+
			"<b>Системный промпт:</b>\n%s",
		escapeHTML(model),
		msgCount,
		created.Format(time.RFC1123),
		sysText,
	)
	b.sendHTML(msg.Chat.ID, text, nil)
}

// handleCallback processes inline keyboard callbacks.
func (b *Bot) handleCallback(query *telego.CallbackQuery) {
	data := query.Data
	parts := strings.SplitN(data, ":", 2)
	if len(parts) < 2 {
		return
	}

	switch parts[0] {
	case "model":
		newModel := parts[1]
		b.mu.Lock()
		b.session.Model = newModel
		b.mu.Unlock()

		_ = b.api.AnswerCallbackQuery(context.Background(), &telego.AnswerCallbackQueryParams{
			CallbackQueryID: query.ID,
			Text:            "Модель: " + newModel,
		})
		if msg, ok := query.Message.(*telego.Message); ok {
			_, _ = b.api.EditMessageText(context.Background(),
				tu.EditMessageText(
					tu.ID(msg.Chat.ID),
					msg.MessageID,
					"✅ Модель переключена: <b>"+escapeHTML(newModel)+"</b>",
				).WithParseMode(telego.ModeHTML),
			)
		}
	}
}

// ── Keyboards ─────────────────────────────────────────────────────────────────

// buildModelKeyboard builds an inline keyboard with all available models.
// The currently active model is marked with ✓.
func buildModelKeyboard(models []string, current string) telego.InlineKeyboardMarkup {
	var rows [][]telego.InlineKeyboardButton
	var row []telego.InlineKeyboardButton

	for i, m := range models {
		label := m
		if m == current {
			label += " ✓"
		}
		row = append(row, telego.InlineKeyboardButton{
			Text:         label,
			CallbackData: "model:" + m,
		})
		if len(row) == 2 || i == len(models)-1 {
			rows = append(rows, row)
			row = nil
		}
	}
	return telego.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// ── Formatting ────────────────────────────────────────────────────────────────

func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}
