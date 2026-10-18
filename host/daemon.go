package host

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"
)

// IsLoopbackAddr reports whether a listen address only accepts local connections.
func IsLoopbackAddr(addr string) bool {
	h, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(h, "[]"))
	return ip != nil && ip.IsLoopback()
}

// Serve serves HTTP(S) on ln until ctx is cancelled, then shuts down gracefully
// and closes the host.
func (h *Host) Serve(ctx context.Context, ln net.Listener) error {
	tcfg, fp, err := h.TLSConfig()
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: h.mux, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute, TLSConfig: tcfg}
	scheme := "http"
	if tcfg != nil {
		scheme = "https"
		h.Logf("TLS enabled (%s); certificate sha256 fingerprint: %s", h.Cfg.TLS, fp)
	} else if !IsLoopbackAddr(ln.Addr().String()) {
		h.Logf("WARNING: listening on %s WITHOUT TLS; API keys and data cross the network in clear text. Use tls=auto or a reverse proxy.", ln.Addr())
	}
	h.Logf("redxd %s node=%s role=%s listening on %s://%s data=%s", Version, h.NodeID, roleName(h.Cluster), scheme, ln.Addr(), h.Cfg.DataDir)
	if h.Cfg.GRPCListen != "" {
		h.Logf("grpc_listen=%s is set but gRPC is a documented STUB in this release (see docs/HOSTING.md); ignoring", h.Cfg.GRPCListen)
	}
	errc := make(chan error, 1)
	go func() {
		if tcfg != nil {
			errc <- srv.ServeTLS(ln, "", "")
		} else {
			errc <- srv.Serve(ln)
		}
	}()
	var serr error
	select {
	case <-ctx.Done():
	case serr = <-errc:
	}
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
	_ = h.Close()
	if serr != nil && !errors.Is(serr, http.ErrServerClosed) {
		return serr
	}
	return nil
}
