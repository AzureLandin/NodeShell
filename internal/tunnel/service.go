// Package tunnel owns on-demand SSH local port forwards: discover remote
// TCP listeners, bind 127.0.0.1, and copy bytes over direct-tcpip. It never
// touches Wails — the App wires the session execer/dialer.
package tunnel

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"

	"nodeshell/internal/apperror"
)

const discoverTimeout = 12 * time.Second

const localHost = "127.0.0.1"

// Tunnel is one live local forward (matches the frontend DTO).
type Tunnel struct {
	ID         string `json:"id"`
	SessionID  string `json:"sessionId"`
	LocalHost  string `json:"localHost"`
	LocalPort  int    `json:"localPort"`
	RemoteAddr string `json:"remoteAddr"`
	RemotePort int    `json:"remotePort"`
}

// Error carries a stable code. Messages never include remote paths.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

func (e *Error) ErrorCode() string { return e.Code }

func errf(code, format string, args ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Execer runs one remote command over a session (production: *sessions.Manager).
type Execer interface {
	Exec(sessionID string, ctx context.Context, command string, timeout time.Duration) (string, error)
}

// Dialer opens a TCP connection through the session (direct-tcpip).
type Dialer interface {
	Dial(sessionID, network, addr string) (net.Conn, error)
}

// Ready reports whether the session can accept a forwarded connection.
type Ready interface {
	CanDial(sessionID string) error
}

// ListenFunc is the local bind seam; production is net.Listen.
type ListenFunc func(network, address string) (net.Listener, error)

// Deps wires a Service. Listen/UUID default to net.Listen and uuid.NewString.
type Deps struct {
	Execer Execer
	Dialer Dialer
	Ready  Ready
	Listen ListenFunc
	UUID   func() string
}

// Service tracks per-session local listeners.
type Service struct {
	execer Execer
	dialer Dialer
	ready  Ready
	listen ListenFunc
	uuid   func() string

	mu      sync.Mutex
	tunnels map[string]*live
	closed  bool
}

type live struct {
	info Tunnel
	ln   net.Listener
}

// New constructs a Service. Execer and Dialer are required for Discover/Start.
func New(d Deps) *Service {
	listen := d.Listen
	if listen == nil {
		listen = net.Listen
	}
	id := d.UUID
	if id == nil {
		id = uuid.NewString
	}
	return &Service{
		execer:  d.Execer,
		dialer:  d.Dialer,
		ready:   d.Ready,
		listen:  listen,
		uuid:    id,
		tunnels: make(map[string]*live),
	}
}

// Discover lists remote TCP listeners for the session.
func (s *Service) Discover(ctx context.Context, sessionID string) ([]Listener, error) {
	if s.execer == nil {
		return nil, errf(apperror.Unknown, "Port discovery is not initialised")
	}
	out, err := s.execer.Exec(sessionID, ctx, DiscoverCommand, discoverTimeout)
	if err != nil {
		return nil, err
	}
	list := ParseListeners(out)
	if list == nil {
		list = []Listener{}
	}
	return list, nil
}

// Start binds 127.0.0.1 (preferring the remote port, else an ephemeral port)
// and forwards accepted connections to remoteAddr:remotePort over SSH.
func (s *Service) Start(sessionID, remoteAddr string, remotePort int) (Tunnel, error) {
	if remotePort <= 0 || remotePort > 65535 {
		return Tunnel{}, errf(apperror.Unknown, "Invalid remote port")
	}
	if s.dialer == nil {
		return Tunnel{}, errf(apperror.Unknown, "Port forwarding is not initialised")
	}
	if s.ready != nil {
		if err := s.ready.CanDial(sessionID); err != nil {
			return Tunnel{}, err
		}
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return Tunnel{}, errf(apperror.Cancelled, "Port forwarding is shutting down")
	}
	for _, l := range s.tunnels {
		if l.info.SessionID == sessionID && l.info.RemoteAddr == remoteAddr && l.info.RemotePort == remotePort {
			info := l.info
			s.mu.Unlock()
			return info, nil
		}
	}
	s.mu.Unlock()

	ln, err := s.listen("tcp", net.JoinHostPort(localHost, strconv.Itoa(remotePort)))
	if err != nil {
		ln, err = s.listen("tcp", net.JoinHostPort(localHost, "0"))
		if err != nil {
			return Tunnel{}, errf(apperror.Unknown, "Failed to listen on a local port")
		}
	}
	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	if !ok || tcpAddr.Port <= 0 {
		_ = ln.Close()
		return Tunnel{}, errf(apperror.Unknown, "Failed to listen on a local port")
	}

	info := Tunnel{
		ID:         s.uuid(),
		SessionID:  sessionID,
		LocalHost:  localHost,
		LocalPort:  tcpAddr.Port,
		RemoteAddr: remoteAddr,
		RemotePort: remotePort,
	}
	entry := &live{info: info, ln: ln}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = ln.Close()
		return Tunnel{}, errf(apperror.Cancelled, "Port forwarding is shutting down")
	}
	s.tunnels[info.ID] = entry
	s.mu.Unlock()

	go s.acceptLoop(entry)
	return info, nil
}

func (s *Service) acceptLoop(entry *live) {
	dialTo := DialAddr(entry.info.RemoteAddr, entry.info.RemotePort)
	for {
		local, err := entry.ln.Accept()
		if err != nil {
			return
		}
		go s.forward(entry.info.SessionID, local, dialTo)
	}
}

func (s *Service) forward(sessionID string, local net.Conn, dialTo string) {
	defer local.Close()
	remote, err := s.dialer.Dial(sessionID, "tcp", dialTo)
	if err != nil {
		return
	}
	defer remote.Close()
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(local, remote)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(remote, local)
		done <- struct{}{}
	}()
	<-done
	_ = local.Close()
	_ = remote.Close()
	<-done
}

// Stop closes one tunnel. Unknown ids are a no-op success.
func (s *Service) Stop(sessionID, tunnelID string) error {
	s.mu.Lock()
	l := s.tunnels[tunnelID]
	if l == nil || l.info.SessionID != sessionID {
		s.mu.Unlock()
		return nil
	}
	delete(s.tunnels, tunnelID)
	s.mu.Unlock()
	_ = l.ln.Close()
	return nil
}

// List returns the live tunnels for the session.
func (s *Service) List(sessionID string) []Tunnel {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Tunnel, 0)
	for _, l := range s.tunnels {
		if l.info.SessionID == sessionID {
			out = append(out, l.info)
		}
	}
	return out
}

// Dispose closes every tunnel for the session (session:closed).
func (s *Service) Dispose(sessionID string) {
	s.mu.Lock()
	var closeList []*live
	for id, l := range s.tunnels {
		if l.info.SessionID == sessionID {
			closeList = append(closeList, l)
			delete(s.tunnels, id)
		}
	}
	s.mu.Unlock()
	for _, l := range closeList {
		_ = l.ln.Close()
	}
}

// DisposeAll closes every tunnel (app shutdown).
func (s *Service) DisposeAll() {
	s.mu.Lock()
	s.closed = true
	all := s.tunnels
	s.tunnels = make(map[string]*live)
	s.mu.Unlock()
	for _, l := range all {
		_ = l.ln.Close()
	}
}
