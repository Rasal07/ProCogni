// Package sms is the gateway adapter. The skeleton ships a logging sender and
// a placeholder for the real provider; nothing above this package knows which
// one is wired.
package sms

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
)

type Sender interface {
	Send(ctx context.Context, phone, message string) error
}

// New picks a sender by name. Unknown names are an error rather than a silent
// fallback, so a typo in PROLEAF_SMS_PROVIDER does not quietly ship OTP codes
// to a log file in production.
func New(provider string, dev bool) (Sender, error) {
	switch provider {
	case "log":
		if !dev {
			return nil, fmt.Errorf("sms: the log sender is refused outside dev")
		}
		return Logger{}, nil
	case "aliyun":
		return nil, fmt.Errorf("sms: provider %q is not implemented in the skeleton", provider)
	default:
		return nil, fmt.Errorf("sms: unknown provider %q", provider)
	}
}

// Logger writes the message, code included, to the application log. Dev only:
// New refuses to build one outside dev.
type Logger struct{}

func (Logger) Send(ctx context.Context, phone, message string) error {
	slog.WarnContext(ctx, "sms (dev sender, message not actually delivered)",
		"phone", phone, "message", message)
	return nil
}

// Capture records messages in memory. Used by tests to read back the code that
// was issued.
type Capture struct {
	mu   sync.Mutex
	sent []Message
}

type Message struct {
	Phone   string
	Message string
}

func (c *Capture) Send(_ context.Context, phone, message string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, Message{Phone: phone, Message: message})
	return nil
}

func (c *Capture) Sent() []Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Message(nil), c.sent...)
}

// Last returns the most recent message sent to phone.
func (c *Capture) Last(phone string) (Message, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.sent) - 1; i >= 0; i-- {
		if c.sent[i].Phone == phone {
			return c.sent[i], true
		}
	}
	return Message{}, false
}
