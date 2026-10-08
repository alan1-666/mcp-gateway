package mcpadapter

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

var errCatalogChanged = errors.New("MCP catalog changed during inspection; rediscover and review before executing")

func (t *protocolTransport) startCatalog() uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.catalogVerified = false
	return t.catalogRevision
}

// The transport mutex orders invalidation against the last pre-dispatch check.
// A notification is only an invalidation hint: it never installs new definitions
// or authorizes a call. Every lease still reads the complete live catalog.
func (t *protocolTransport) catalogVersion() uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.catalogRevision
}

func (t *protocolTransport) catalogChanged() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.catalogRevision++
	t.catalogVerified = false
}

func (t *protocolTransport) verifyCatalog(version uint64) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.catalogRevision != version {
		return errCatalogChanged
	}
	t.catalogVerified = true
	return nil
}

func (t *protocolTransport) catalogReusable() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.catalogVerified
}

// Observe complete SSE frames before returning their bytes to the SDK. SDK
// notification handlers run asynchronously; using only their callback could
// let a tools/list response unblock dispatch before invalidation is processed.
// This scanner stores at most the bounded original POST response, has linear
// input cost and never treats incomplete frames or tool text as notifications.
type catalogNotificationScanner struct {
	line    []byte
	data    []string
	event   string
	skipLF  bool
	started bool
	changed func()
}

func (s *catalogNotificationScanner) read(data []byte) {
	for _, ch := range data {
		if s.skipLF {
			s.skipLF = false
			if ch == '\n' {
				continue
			}
		}
		switch ch {
		case '\r', '\n':
			s.finishLine()
			s.skipLF = ch == '\r'
		default:
			s.line = append(s.line, ch)
		}
	}
}

func (s *catalogNotificationScanner) finishLine() {
	line := s.line
	s.line = nil
	if !s.started {
		line = bytes.TrimPrefix(line, []byte{0xef, 0xbb, 0xbf})
		s.started = true
	}
	if len(line) == 0 {
		if s.event == "" || s.event == "message" {
			var message struct {
				JSONRPC string          `json:"jsonrpc"`
				Method  string          `json:"method"`
				ID      json.RawMessage `json:"id"`
				Result  json.RawMessage `json:"result"`
				Error   json.RawMessage `json:"error"`
			}
			if json.Unmarshal([]byte(strings.Join(s.data, "\n")), &message) == nil && message.JSONRPC == "2.0" && message.Method == "notifications/tools/list_changed" && len(message.ID) == 0 && len(message.Result) == 0 && len(message.Error) == 0 {
				s.changed()
			}
		}
		s.data, s.event = nil, ""
		return
	}
	field, value, _ := strings.Cut(string(line), ":")
	value = strings.TrimPrefix(value, " ")
	switch field {
	case "data":
		s.data = append(s.data, value)
	case "event":
		s.event = value
	}
}
