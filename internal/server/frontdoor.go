package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
)

// Frontdoor is the HTTP listener of a process that is still starting.
//
// serve and run-worker open their port only after the database answers and the
// migrations have run. Until then /live had nothing listening, so a liveness
// probe killed a process that was only waiting for PostgreSQL or for another
// replica's migration — and a long migration was interrupted on every attempt.
//
// Opened before the store, the frontdoor answers /live with 200 (the process is
// alive) and everything else with 503 (it cannot serve yet). The real handler
// replaces the startup one on the same listener, so there is no gap in which the
// port is closed.
type Frontdoor struct {
	httpSrv *http.Server
	handler atomic.Pointer[http.Handler]
	errCh   chan error
	addr    net.Addr
}

// OpenFrontdoor starts listening on addr with the startup handler. TLS is
// configured when a certificate and key are given.
func OpenFrontdoor(addr string, cfg Config, tlsCert, tlsKey string) (*Frontdoor, error) {
	fd := &Frontdoor{errCh: make(chan error, 1)}
	var starting http.Handler = http.HandlerFunc(startupHandler)
	fd.handler.Store(&starting)
	fd.httpSrv = &http.Server{
		Addr:              addr,
		Handler:           http.HandlerFunc(fd.serveHTTP),
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
	}
	if tlsCert != "" && tlsKey != "" {
		cert, err := tls.LoadX509KeyPair(tlsCert, tlsKey)
		if err != nil {
			return nil, fmt.Errorf("load TLS key pair: %w", err)
		}
		fd.httpSrv.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	fd.addr = ln.Addr()
	go func() {
		slog.Info("listening", "addr", addr)
		var err error
		if fd.httpSrv.TLSConfig != nil {
			err = fd.httpSrv.ServeTLS(ln, "", "")
		} else {
			err = fd.httpSrv.Serve(ln)
		}
		if err != nil && err != http.ErrServerClosed {
			fd.errCh <- err
		}
	}()
	return fd, nil
}

func (fd *Frontdoor) serveHTTP(w http.ResponseWriter, r *http.Request) {
	(*fd.handler.Load()).ServeHTTP(w, r)
}

// SetHandler hands the listener over to the started service.
func (fd *Frontdoor) SetHandler(h http.Handler) { fd.handler.Store(&h) }

// Addr is the address the listener is bound to.
func (fd *Frontdoor) Addr() net.Addr { return fd.addr }

// Err reports a listener failure.
func (fd *Frontdoor) Err() <-chan error { return fd.errCh }

// Shutdown stops the listener gracefully.
func (fd *Frontdoor) Shutdown(ctx context.Context) error { return fd.httpSrv.Shutdown(ctx) }

func startupHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/live" {
		writeJSON(w, http.StatusOK, map[string]any{"status": "starting"})
		return
	}
	writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "starting"})
}
