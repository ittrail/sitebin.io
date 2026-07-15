// Package ftp serves each site's files over FTP as an optional alternative to
// WebDAV. A client logs in with the site's edit UUID as the username and the
// edit password as the password; the session is confined to that site's files
// with the same quota and path rules as every other write path.
//
// FTP is plaintext unless FTPS (explicit AUTH TLS) is configured, so it is
// off by default at both the instance and per-site level.
package ftp

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"

	ftpserver "github.com/fclairamb/ftpserverlib"

	"github.com/ittrail/sitebin.io/internal/config"
)

// Authenticator verifies an FTP login and returns the directory to serve plus
// the site's effective quota caps. Implemented by the HTTP API (which reuses
// its edit-password rate limiting and verification cache).
type Authenticator interface {
	FTPAuth(editID, password, clientIP string) (dir string, maxBytes int64, maxFiles int, err error)
}

// Server wraps the FTP server for one instance.
type Server struct {
	srv *ftpserver.FtpServer
}

// New builds an FTP server from config, authenticating via auth.
func New(cfg config.Config, auth Authenticator) (*Server, error) {
	d := &driver{cfg: cfg, auth: auth}
	if cfg.FTPTLSCert != "" {
		cert, err := tls.LoadX509KeyPair(cfg.FTPTLSCert, cfg.FTPTLSKey)
		if err != nil {
			return nil, fmt.Errorf("ftp tls: %w", err)
		}
		d.tlsConfig = &tls.Config{Certificates: []tls.Certificate{cert}}
	}
	return &Server{srv: ftpserver.NewFtpServer(d)}, nil
}

// ListenAndServe runs the FTP server until Stop is called.
func (s *Server) ListenAndServe() error { return s.srv.ListenAndServe() }

// Stop shuts the FTP server down.
func (s *Server) Stop() error { return s.srv.Stop() }

// driver implements ftpserverlib.MainDriver.
type driver struct {
	cfg       config.Config
	auth      Authenticator
	tlsConfig *tls.Config
}

func (d *driver) GetSettings() (*ftpserver.Settings, error) {
	s := &ftpserver.Settings{
		ListenAddr: d.cfg.FTPAddr,
		PublicHost: d.cfg.FTPPublicHost,
		PassiveTransferPortRange: &ftpserver.PortRange{
			Start: d.cfg.FTPPasvMin,
			End:   d.cfg.FTPPasvMax,
		},
	}
	if d.tlsConfig != nil {
		s.TLSRequired = ftpserver.MandatoryEncryption
	}
	return s, nil
}

func (d *driver) ClientConnected(cc ftpserver.ClientContext) (string, error) {
	return "Sitebin FTP — log in with your site's edit UUID and edit password", nil
}

func (d *driver) ClientDisconnected(cc ftpserver.ClientContext) {}

func (d *driver) AuthUser(cc ftpserver.ClientContext, user, pass string) (ftpserver.ClientDriver, error) {
	ip := ""
	if addr := cc.RemoteAddr(); addr != nil {
		if host, _, err := net.SplitHostPort(addr.String()); err == nil {
			ip = host
		} else {
			ip = addr.String()
		}
	}
	dir, maxBytes, maxFiles, err := d.auth.FTPAuth(user, pass, ip)
	if err != nil {
		return nil, err
	}
	return newQuotaFs(dir, maxBytes, maxFiles), nil
}

func (d *driver) GetTLSConfig() (*tls.Config, error) {
	if d.tlsConfig == nil {
		return nil, errors.New("FTPS not configured")
	}
	return d.tlsConfig, nil
}
