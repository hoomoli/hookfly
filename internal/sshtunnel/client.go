package sshtunnel

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/hoomoli/hookfly/internal/config"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Tunnel owns immutable authentication material, but no idle sockets.
type Tunnel struct {
	address, user, knownHosts string
	signer                    ssh.Signer
}

func New(cfg config.SSHTunnel) (*Tunnel, error) {
	if cfg.HostKeyPolicy != "" && cfg.HostKeyPolicy != "accept_new" {
		return nil, errors.New("unsupported SSH host key policy")
	}
	key, err := os.ReadFile(cfg.PrivateKeyFile)
	if err != nil {
		return nil, errors.New("cannot read SSH private key")
	}
	var signer ssh.Signer
	if cfg.PrivateKeyPassphrase != "" {
		signer, err = ssh.ParsePrivateKeyWithPassphrase(key, []byte(cfg.PrivateKeyPassphrase))
	} else {
		signer, err = ssh.ParsePrivateKey(key)
	}
	if err != nil {
		return nil, errors.New("invalid SSH private key or passphrase")
	}
	if _, err = os.Stat(cfg.KnownHostsFile); err == nil {
		if _, err = knownhosts.New(cfg.KnownHostsFile); err != nil {
			return nil, errTrust
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, errTrust
	}
	port := cfg.Port
	if port == 0 {
		port = 22
	}
	return &Tunnel{address: net.JoinHostPort(cfg.Host, strconv.Itoa(port)), user: cfg.User, knownHosts: cfg.KnownHostsFile, signer: signer}, nil
}

// HTTPClient pins a transport to one origin. Destination DNS is resolved by SSH.
func (t *Tunnel) HTTPClient(raw string) (*http.Client, error) {
	endpoint, err := url.Parse(raw)
	if err != nil || endpoint.Hostname() == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.User != nil {
		return nil, errors.New("invalid SSH HTTP destination")
	}
	port := endpoint.Port()
	if port == "" {
		port = "80"
		if endpoint.Scheme == "https" {
			port = "443"
		}
	}
	return &http.Client{Transport: &transport{tunnel: t, scheme: endpoint.Scheme, host: endpoint.Host, address: net.JoinHostPort(endpoint.Hostname(), port)}}, nil
}

type transport struct {
	tunnel                *Tunnel
	scheme, host, address string
}

func (t *transport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme != t.scheme || request.URL.Host != t.host {
		return nil, errors.New("SSH destination does not match configured origin")
	}
	ctx, cancel := context.WithTimeout(request.Context(), 30*time.Second)
	// A fresh HTTP transport cannot retry a request on a previously used socket.
	// Its socket and SSH session share the response body's lifetime.
	httpTransport := &http.Transport{DisableKeepAlives: true, DisableCompression: true, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 30 * time.Second}
	httpTransport.DialContext = func(_ context.Context, network, address string) (net.Conn, error) {
		if address != t.address {
			return nil, dialError("SSH destination is not allowed")
		}
		return t.tunnel.dial(ctx, address)
	}
	response, err := httpTransport.RoundTrip(request.Clone(ctx))
	if err != nil {
		cancel()
		httpTransport.CloseIdleConnections()
		return nil, err
	}
	response.Body = &responseBody{ReadCloser: response.Body, finish: func() { cancel(); httpTransport.CloseIdleConnections() }}
	return response, nil
}

type responseBody struct {
	io.ReadCloser
	finish func()
	once   sync.Once
}

func (b *responseBody) Close() error { err := b.ReadCloser.Close(); b.once.Do(b.finish); return err }
func (b *responseBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.once.Do(b.finish)
	}
	return n, err
}

func dialError(message string) error {
	return &net.OpError{Op: "dial", Net: "ssh", Err: errors.New(message)}
}

func (t *Tunnel) dial(ctx context.Context, destination string) (net.Conn, error) {
	raw, err := (&net.Dialer{}).DialContext(ctx, "tcp", t.address)
	if err != nil {
		return nil, dialError("SSH jump host connection failed")
	}
	stop := context.AfterFunc(ctx, func() { raw.Close() })
	success := false
	defer func() {
		if !success {
			stop()
			raw.Close()
		}
	}()
	handshakeDeadline := time.Now().Add(10 * time.Second)
	if deadline, ok := ctx.Deadline(); ok && deadline.Before(handshakeDeadline) {
		handshakeDeadline = deadline
	}
	raw.SetDeadline(handshakeDeadline)
	cfg := &ssh.ClientConfig{User: t.user, Auth: []ssh.AuthMethod{ssh.PublicKeys(t.signer)}, HostKeyCallback: func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		return acceptNew(ctx, t.knownHosts, hostname, remote, key)
	}}
	conn, channels, requests, err := ssh.NewClientConn(raw, t.address, cfg)
	if err != nil {
		return nil, dialError("SSH handshake, authentication, or host trust failed")
	}
	client := ssh.NewClient(conn, channels, requests)
	raw.SetDeadline(time.Time{})
	if destination == "" {
		success = true
		return &ownedConn{Conn: raw, raw: raw, client: client, stop: stop}, nil
	}
	forwarded, err := client.DialContext(ctx, "tcp", destination)
	if err != nil {
		client.Close()
		return nil, dialError("SSH forwarding channel failed")
	}
	success = true
	return &ownedConn{Conn: forwarded, raw: raw, client: client, stop: stop}, nil
}

type ownedConn struct {
	net.Conn
	raw    net.Conn
	client *ssh.Client
	stop   func() bool
	once   sync.Once
}

func (c *ownedConn) Close() error {
	c.once.Do(func() { c.stop(); c.raw.Close(); c.Conn.Close(); c.client.Close() })
	return nil
}

// SSH channels do not implement deadlines. Each channel has its own SSH socket.
func (c *ownedConn) SetDeadline(t time.Time) error      { return c.raw.SetDeadline(t) }
func (c *ownedConn) SetReadDeadline(t time.Time) error  { return c.raw.SetReadDeadline(t) }
func (c *ownedConn) SetWriteDeadline(t time.Time) error { return c.raw.SetWriteDeadline(t) }

// Check authenticates the jump host and opens each configured forwarding destination.
// It never sends HTTP requests or deployment commands.
func (t *Tunnel) Check(ctx context.Context, destinations []string) error {
	if len(destinations) == 0 {
		conn, err := t.dial(ctx, "")
		if err != nil {
			return err
		}
		return conn.Close()
	}
	seen := map[string]bool{}
	for _, raw := range destinations {
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" {
			return errors.New("invalid SSH destination")
		}
		port := u.Port()
		if port == "" {
			port = "80"
			if u.Scheme == "https" {
				port = "443"
			}
		}
		address := net.JoinHostPort(u.Hostname(), port)
		if seen[address] {
			continue
		}
		seen[address] = true
		conn, err := t.dial(ctx, address)
		if err != nil {
			return err
		}
		conn.Close()
	}
	return nil
}
