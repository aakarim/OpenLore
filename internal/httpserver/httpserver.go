package httpserver

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"

	gossh "golang.org/x/crypto/ssh"
)

// MuxExtender can register additional handlers on the HTTP mux.
type MuxExtender interface {
	RegisterHTTPHandlers(mux *http.ServeMux)
}

// Config holds HTTP server configuration.
type Config struct {
	Port           int
	TLSCert        string
	TLSKey         string
	ClientCABundle string
	HostKeyPath    string
	SSHPort        int
	Logger         *slog.Logger
	Extenders      []MuxExtender
	ExtraHandlers  map[string]http.Handler
}

// Server is the HTTP front page server.
type Server struct {
	srv    *http.Server
	config Config
}

// New creates a new HTTP server serving the given filesystem.
func New(fsys fs.FS, cfg Config) (*Server, error) {
	mux := http.NewServeMux()

	if cfg.HostKeyPath != "" {
		mux.HandleFunc("/host-key", hostKeyHandler(cfg.HostKeyPath, cfg.SSHPort))
	}

	for _, ext := range cfg.Extenders {
		ext.RegisterHTTPHandlers(mux)
	}
	for pattern, handler := range cfg.ExtraHandlers {
		mux.Handle(pattern, handler)
	}

	mux.Handle("/", http.FileServer(http.FS(fsys)))

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Port),
		Handler: mux,
	}

	if cfg.TLSCert != "" && cfg.TLSKey != "" {
		srv.TLSConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
		}
		if cfg.ClientCABundle != "" {
			pemBytes, err := os.ReadFile(cfg.ClientCABundle)
			if err != nil {
				return nil, fmt.Errorf("reading mTLS CA bundle: %w", err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pemBytes) {
				return nil, fmt.Errorf("mTLS CA bundle contains no certificates")
			}
			srv.TLSConfig.ClientAuth = tls.VerifyClientCertIfGiven
			srv.TLSConfig.ClientCAs = pool
		}
	} else if cfg.ClientCABundle != "" {
		return nil, fmt.Errorf("mTLS requires OpenLore TLS termination")
	}

	return &Server{srv: srv, config: cfg}, nil
}

// hostKeyHandler returns an HTTP handler that serves the SSH host public key.
// The SSH port is included in the X-SSH-Port header so clients can construct
// correct known_hosts entries.
func hostKeyHandler(hostKeyPath string, sshPort int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		keyBytes, err := os.ReadFile(hostKeyPath)
		if err != nil {
			http.Error(w, "host key not available", http.StatusInternalServerError)
			return
		}
		signer, err := gossh.ParsePrivateKey(keyBytes)
		if err != nil {
			http.Error(w, "host key not available", http.StatusInternalServerError)
			return
		}
		pubKey := gossh.MarshalAuthorizedKey(signer.PublicKey())
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("X-SSH-Port", fmt.Sprintf("%d", sshPort))
		w.Write(pubKey)
	}
}

// Start starts the HTTP server in a goroutine.
func (s *Server) Start() {
	logger := s.config.Logger
	if logger == nil {
		logger = slog.Default()
	}

	go func() {
		if s.config.TLSCert != "" && s.config.TLSKey != "" {
			logger.Info("HTTP server starting (TLS)", "port", s.config.Port)
			if err := s.srv.ListenAndServeTLS(s.config.TLSCert, s.config.TLSKey); err != nil && err != http.ErrServerClosed {
				logger.Error("HTTP server error", "error", err)
			}
		} else {
			logger.Info("HTTP server starting", "port", s.config.Port)
			if err := s.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logger.Error("HTTP server error", "error", err)
			}
		}
	}()
}

// Shutdown gracefully shuts down the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}
