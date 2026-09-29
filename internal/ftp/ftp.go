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
	"os"

	ftpserver "github.com/fclairamb/ftpserverlib"
	"github.com/spf13/afero"

	"github.com/ittrail/sitebin.io/internal/config"
)

// Authenticator verifies an FTP login and returns the session: the directory
// to serve, the site's effective quota caps and its write guard. Implemented
// by the HTTP API (which reuses its edit-password rate limiting and
// verification cache).
type Authenticator interface {
	FTPAuth(editID, password, clientIP string) (Session, error)
}

// Session is what a successful login opens.
type Session struct {
	Dir      string
	MaxBytes int64
	MaxFiles int
	// Guard stages every write and checks every rename, so the abuse guard
	// settles its verdict before a transferred file is visible. Nil writes
	// straight into Dir (tests of the quota rules alone).
	Guard Guard
}

// File is an open file as the FTP server needs it.
type File = afero.File

// Guard is how a session writes into its site. rel is native and relative
// to the session's directory, already validated as an upload path.
type Guard interface {
	// Stage opens rel for writing; the content becomes visible on Close,
	// which fails when the abuse guard held the site.
	Stage(rel string, flag int, perm os.FileMode) (File, error)
	// Rename renames within the site, checking a file whose kind changes.
	Rename(oldRel, newRel string) error
}

// Recorder is implemented by an Authenticator that keeps a record of every
// write (the HTTP API's provenance log). OPTIONAL: without it FTP writes are
// simply not recorded. action is one of the provenance actions (upload,
// delete-file, mkdir, move); path is the site-relative name.
type Recorder interface {
	FTPWrote(editID, clientIP, action, path string)
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
		// A control connection that goes quiet is closed, and a data
		// connection that never arrives stops being waited for: without
		// these, every half-open session is held for ever.
		IdleTimeout:       300,
		ConnectionTimeout: 30,
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
	sess, err := d.auth.FTPAuth(user, pass, ip)
	if err != nil {
		return nil, err
	}
	q := newQuotaFs(sess.Dir, sess.MaxBytes, sess.MaxFiles)
	q.guard = sess.Guard
	if rec, ok := d.auth.(Recorder); ok {
		q.onWrite = func(action, path string) { rec.FTPWrote(user, ip, action, path) }
	}
	return q, nil
}

func (d *driver) GetTLSConfig() (*tls.Config, error) {
	if d.tlsConfig == nil {
		return nil, errors.New("FTPS not configured")
	}
	return d.tlsConfig, nil
}
