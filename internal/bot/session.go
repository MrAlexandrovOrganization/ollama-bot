package bot

import (
	"ollama-bot/internal/ollama"
	"time"
)

// Session holds the ongoing LLM conversation state.
type Session struct {
	Model        string
	SystemPrompt string
	Messages     []ollama.Message
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func newSession(model string) *Session {
	now := time.Now()
	return &Session{
		Model:     model,
		Messages:  []ollama.Message{},
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func (s *Session) reset() {
	s.Messages = []ollama.Message{}
	s.UpdatedAt = time.Now()
}

func (s *Session) addUser(content string, images ...string) {
	msg := ollama.Message{Role: "user", Content: content}
	if len(images) > 0 {
		msg.Images = images
	}
	s.Messages = append(s.Messages, msg)
	s.UpdatedAt = time.Now()
}

func (s *Session) addAssistant(content string) {
	s.Messages = append(s.Messages, ollama.Message{Role: "assistant", Content: content})
	s.UpdatedAt = time.Now()
}

// buildMessages prepends the system prompt (if set) to the history.
func (s *Session) buildMessages() []ollama.Message {
	if s.SystemPrompt == "" {
		return s.Messages
	}
	result := make([]ollama.Message, 0, len(s.Messages)+1)
	result = append(result, ollama.Message{Role: "system", Content: s.SystemPrompt})
	result = append(result, s.Messages...)
	return result
}

// popLastUser removes the last user message from history.
// Used to roll back after a failed LLM request.
func (s *Session) popLastUser() {
	if len(s.Messages) > 0 {
		s.Messages = s.Messages[:len(s.Messages)-1]
	}
}
