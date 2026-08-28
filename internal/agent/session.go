package agent

import (
	"time"

	"github.com/google/uuid"
)

// Session is a persistent conversation identified by ID. Messages are
// append-only in insertion order.
type Session struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Messages  []Message `json:"messages"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// NewSession constructs an empty Session with a fresh UUID.
func NewSession(title string) *Session {
	now := time.Now().UTC()
	return &Session{
		ID:        uuid.NewString(),
		Title:     title,
		Messages:  nil,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// Append records a Message on the Session and advances UpdatedAt.
func (s *Session) Append(m Message) {
	s.Messages = append(s.Messages, m)
	s.UpdatedAt = time.Now().UTC()
}

// Last returns the final Message on the Session, or the zero value and
// false if the Session is empty.
func (s *Session) Last() (Message, bool) {
	if len(s.Messages) == 0 {
		return Message{}, false
	}
	return s.Messages[len(s.Messages)-1], true
}
