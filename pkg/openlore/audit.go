package openlore

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type AuditEvent struct {
	Time        time.Time      `json:"time"`
	Type        string         `json:"type"`
	Attribution Attribution    `json:"attribution"`
	Details     map[string]any `json:"details,omitempty"`
}

type AuditLog interface {
	Record(context.Context, AuditEvent) error
}

type JSONLAuditLog struct {
	mu   sync.Mutex
	path string
}

func NewJSONLAuditLog(path string) *JSONLAuditLog { return &JSONLAuditLog{path: path} }

func (l *JSONLAuditLog) Record(_ context.Context, event AuditEvent) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(event); err != nil {
		return err
	}
	return f.Sync()
}
