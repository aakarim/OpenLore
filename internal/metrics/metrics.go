package metrics

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
)

// Metrics tracks server statistics.
type Metrics struct {
	ActiveSessions atomic.Int64
	TotalSessions  atomic.Int64
	TotalCommands  atomic.Int64
	TotalBytes     atomic.Int64
}

type snapshot struct {
	ActiveSessions int64 `json:"active_sessions"`
	TotalSessions  int64 `json:"total_sessions"`
	TotalCommands  int64 `json:"total_commands"`
	TotalBytes     int64 `json:"total_bytes"`
}

// Handler returns an HTTP handler that serves JSON metrics at GET /metrics.
func (m *Metrics) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		snap := snapshot{
			ActiveSessions: m.ActiveSessions.Load(),
			TotalSessions:  m.TotalSessions.Load(),
			TotalCommands:  m.TotalCommands.Load(),
			TotalBytes:     m.TotalBytes.Load(),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(snap)
	})
	return mux
}

// StartServer starts the metrics HTTP server. Returns nil if port is 0.
func StartServer(port int, m *Metrics, logger *slog.Logger) *http.Server {
	return StartHandlerServer(port, m.Handler(), logger)
}

func StartHandlerServer(port int, handler http.Handler, logger *slog.Logger) *http.Server {
	if port == 0 {
		return nil
	}

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: handler,
	}

	go func() {
		logger.Info("metrics server starting", "port", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("metrics server error", "error", err)
		}
	}()

	return srv
}
