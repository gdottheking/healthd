package roles

import (
	"errors"

	"connection_monitor/internal/protocol"
)

// MessageDispatcher decodes a newline-framed request line and routes it to the
// registered handler for its type. It centralizes malformed-input and
// version-mismatch handling so every handler sees a well-formed, version-valid
// request. It is safe for concurrent use: handlers are fixed at construction
// and only read.
type MessageDispatcher struct {
	handlers map[string]MessageHandler
}

// NewMessageDispatcher builds a dispatcher from the given handlers, keyed by
// each handler's Type. A later handler with a duplicate type overrides an
// earlier one.
func NewMessageDispatcher(handlers ...MessageHandler) *MessageDispatcher {
	m := make(map[string]MessageHandler, len(handlers))
	for _, h := range handlers {
		m[h.Type()] = h
	}
	return &MessageDispatcher{handlers: m}
}

// Dispatch parses one request line and returns the response to write back. It
// never panics on malformed input: a decode failure yields a malformed-request
// error, a version mismatch yields an error echoing the id, and an unknown
// request type yields an unsupported-type error.
func (d *MessageDispatcher) Dispatch(line []byte) protocol.Response {
	req, err := protocol.DecodeRequest(line)
	if err != nil {
		if errors.Is(err, protocol.ErrVersionMismatch) {
			return protocol.NewError(req.ID, "unsupported protocol version")
		}
		return protocol.NewError("", "malformed request")
	}
	h, ok := d.handlers[req.Type]
	if !ok {
		return protocol.NewError(req.ID, "unsupported request type")
	}
	return h.Handle(req)
}
