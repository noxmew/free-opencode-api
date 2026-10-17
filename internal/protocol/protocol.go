package protocol

import "strings"

type Protocol string

const (
	Chat      Protocol = "chat"
	Responses Protocol = "responses"
	Anthropic Protocol = "anthropic"
)

func (p Protocol) Valid() bool {
	switch p {
	case Chat, Responses, Anthropic:
		return true
	default:
		return false
	}
}

func (p Protocol) Path() string {
	switch p {
	case Responses:
		return "/responses"
	case Anthropic:
		return "/messages"
	default:
		return "/chat/completions"
	}
}

func (p Protocol) String() string {
	return string(p)
}

func Parse(value string) Protocol {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "responses", "response":
		return Responses
	case "anthropic", "messages", "message":
		return Anthropic
	default:
		return Chat
	}
}
