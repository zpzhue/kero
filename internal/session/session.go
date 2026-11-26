package session

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"kero/internal/provider"
)

const eventVersion = 1

type event struct {
	Version int             `json:"version"`
	Kind    string          `json:"kind"`
	Data    json.RawMessage `json:"data"`
}

type sessionInfo struct {
	Root string `json:"root"`
}

// Session owns an exclusive lock and an append-only event log until Close.
type Session struct {
	mu      sync.Mutex
	id      string
	file    *os.File
	lock    net.Listener
	pending []string
	failed  error
	closed  bool
}

// Open creates a session when id is empty, or resumes an existing session.
// The root of a resumed session must be the same directory used to create it.
func Open(id, root string) (*Session, []provider.Message, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve session root: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve session root: %w", err)
	}
	config, err := os.UserConfigDir()
	if err != nil {
		return nil, nil, fmt.Errorf("find session directory: %w", err)
	}
	dir := filepath.Join(config, "kero", "sessions")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, nil, fmt.Errorf("create session directory: %w", err)
	}
	creating := id == ""
	if creating {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, nil, fmt.Errorf("generate session ID: %w", err)
		}
		id = hex.EncodeToString(random[:])
	} else if !validID(id) {
		return nil, nil, errors.New("invalid session ID")
	}

	path := filepath.Join(dir, id+".jsonl")
	// A loopback listener is an OS-managed lock: it is released even if the
	// process exits unexpectedly, unlike a lock file left behind after a crash.
	lockPath := path
	if runtime.GOOS == "windows" {
		lockPath = strings.ToLower(lockPath)
	}
	sum := sha256.Sum256([]byte(lockPath))
	port := 32768 + (int(sum[0])<<8|int(sum[1]))%32768
	lock, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, nil, fmt.Errorf("session is locked or its lock is unavailable: %w", err)
	}
	flags := os.O_RDWR
	if creating {
		flags |= os.O_CREATE | os.O_EXCL
	}
	file, err := os.OpenFile(path, flags, 0600)
	if err != nil {
		lock.Close()
		return nil, nil, fmt.Errorf("open session: %w", err)
	}
	s := &Session{id: id, file: file, lock: lock}
	fail := func(err error) (*Session, []provider.Message, error) {
		file.Close()
		if creating {
			os.Remove(path)
		}
		lock.Close()
		return nil, nil, err
	}
	if creating {
		if err := s.writeEvent("session", sessionInfo{Root: root}); err != nil {
			return fail(err)
		}
		return s, nil, nil
	}

	messages, err := s.load(root)
	if err != nil {
		return fail(err)
	}
	// A recorded call may have run before interruption. Never run it again:
	// complete the protocol with a durable unknown-status result instead.
	for len(s.pending) > 0 {
		message := provider.Message{Role: "tool", ToolCallID: s.pending[0], Content: "execution status unknown"}
		if err := s.appendMessage(message); err != nil {
			return fail(err)
		}
		messages = append(messages, message)
	}
	return s, messages, nil
}

func validID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func (s *Session) load(root string) ([]provider.Message, error) {
	reader := bufio.NewReader(s.file)
	var messages []provider.Message
	var offset int64
	first := true
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("read session: %w", err)
			}
			if len(line) > 0 {
				backup, backupErr := os.CreateTemp(filepath.Dir(s.file.Name()), s.id+"-incomplete-*.jsonl")
				if backupErr != nil {
					return nil, fmt.Errorf("preserve incomplete event: %w", backupErr)
				}
				if _, backupErr = backup.Write(line); backupErr != nil {
					backup.Close()
					return nil, fmt.Errorf("preserve incomplete event: %w", backupErr)
				}
				if backupErr = backup.Close(); backupErr != nil {
					return nil, backupErr
				}
				fmt.Fprintf(os.Stderr, "Incomplete session event saved to %s\n", backup.Name())
				if err := s.file.Truncate(offset); err != nil {
					return nil, fmt.Errorf("truncate incomplete session event: %w", err)
				}
			}
			break
		}
		offset += int64(len(line))
		var e event
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("invalid session event: %w", err)
		}
		if e.Version != eventVersion {
			return nil, fmt.Errorf("unsupported session event version %d", e.Version)
		}
		if first {
			first = false
			var info sessionInfo
			if e.Kind != "session" || json.Unmarshal(e.Data, &info) != nil || info.Root == "" {
				return nil, errors.New("invalid session header")
			}
			if info.Root != root && !(runtime.GOOS == "windows" && strings.EqualFold(info.Root, root)) {
				return nil, fmt.Errorf("session belongs to a different root: %s", info.Root)
			}
			continue
		}
		switch e.Kind {
		case "session":
			return nil, errors.New("unexpected session header")
		case "message":
			var message provider.Message
			if err := json.Unmarshal(e.Data, &message); err != nil {
				return nil, fmt.Errorf("invalid session message: %w", err)
			}
			pending, err := nextPending(s.pending, message)
			if err != nil {
				return nil, fmt.Errorf("invalid session history: %w", err)
			}
			s.pending = pending
			messages = append(messages, message)
		default:
			if strings.TrimSpace(e.Kind) == "" {
				return nil, errors.New("invalid session event kind")
			}
		}
	}
	if first {
		return nil, errors.New("session has no complete header")
	}
	if _, err := s.file.Seek(0, io.SeekEnd); err != nil {
		return nil, fmt.Errorf("seek session: %w", err)
	}
	return messages, nil
}

// ID returns the session identifier.
func (s *Session) ID() string { return s.id }

// AppendMessage records a message durably and maintains tool-call ordering.
func (s *Session) AppendMessage(message provider.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendMessage(message)
}

func (s *Session) appendMessage(message provider.Message) error {
	pending, err := nextPending(s.pending, message)
	if err != nil {
		return err
	}
	if err := s.writeEvent("message", message); err != nil {
		return err
	}
	s.pending = pending
	return nil
}

func nextPending(pending []string, message provider.Message) ([]string, error) {
	if message.Role == "tool" {
		if len(message.ToolCalls) != 0 || message.ToolCallID == "" {
			return nil, errors.New("invalid tool result")
		}
		index := -1
		for i, id := range pending {
			if id == message.ToolCallID {
				index = i
				break
			}
		}
		if index < 0 {
			return nil, errors.New("tool result has no pending call")
		}
		next := make([]string, 0, len(pending)-1)
		next = append(next, pending[:index]...)
		next = append(next, pending[index+1:]...)
		return next, nil
	}
	if len(pending) != 0 {
		return nil, errors.New("pending tool calls must have results before the next message")
	}
	if message.Role == "" || message.ToolCallID != "" || len(message.ToolCalls) > 0 && message.Role != "assistant" {
		return nil, errors.New("invalid message")
	}
	if len(message.ToolCalls) == 0 {
		return nil, nil
	}
	next := make([]string, 0, len(message.ToolCalls))
	seen := make(map[string]struct{}, len(message.ToolCalls))
	for _, call := range message.ToolCalls {
		if call.ID == "" {
			return nil, errors.New("tool call has no ID")
		}
		if _, exists := seen[call.ID]; exists {
			return nil, errors.New("duplicate tool call ID")
		}
		seen[call.ID] = struct{}{}
		next = append(next, call.ID)
	}
	return next, nil
}

// AppendEvent records application metadata without adding it to chat history.
func (s *Session) AppendEvent(kind string, data any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(kind) == "" || kind == "session" || kind == "message" {
		return errors.New("invalid or reserved session event kind")
	}
	return s.writeEvent(kind, data)
}

func (s *Session) writeEvent(kind string, data any) error {
	if s.closed {
		return errors.New("session is closed")
	}
	if s.failed != nil {
		return fmt.Errorf("session write previously failed: %w", s.failed)
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("encode session event data: %w", err)
	}
	line, err := json.Marshal(event{Version: eventVersion, Kind: kind, Data: payload})
	if err != nil {
		return fmt.Errorf("encode session event: %w", err)
	}
	line = append(line, '\n')
	if _, err = s.file.Write(line); err == nil {
		err = s.file.Sync()
	}
	if err != nil {
		s.failed = err
		return fmt.Errorf("write session event: %w", err)
	}
	return nil
}

// Close releases the session file and its exclusive lock. It is idempotent.
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return errors.Join(s.file.Close(), s.lock.Close())
}
