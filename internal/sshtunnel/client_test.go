package sshtunnel

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoomoli/hookfly/internal/config"
	"golang.org/x/crypto/ssh"
)

// The server deliberately resolves a name unavailable to the local resolver.
func jumpServer(t *testing.T, authorized ssh.PublicKey, destination string, deny bool) (string, *atomic.Int32) {
	t.Helper()
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if !bytes.Equal(key.Marshal(), authorized.Marshal()) {
			return nil, os.ErrPermission
		}
		return nil, nil
	}}
	cfg.AddHostKey(testKey(t))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var active atomic.Int32
	var wg sync.WaitGroup
	var sockets sync.Map
	t.Cleanup(func() {
		listener.Close()
		sockets.Range(func(k, v any) bool { k.(net.Conn).Close(); return true })
		wg.Wait()
	})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			raw, err := listener.Accept()
			if err != nil {
				return
			}
			sockets.Store(raw, true)
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer sockets.Delete(raw)
				defer raw.Close()
				active.Add(1)
				defer active.Add(-1)
				conn, channels, requests, err := ssh.NewServerConn(raw, cfg)
				if err != nil {
					return
				}
				defer conn.Close()
				go ssh.DiscardRequests(requests)
				for channel := range channels {
					var payload struct {
						Host       string
						Port       uint32
						Origin     string
						OriginPort uint32
					}
					if channel.ChannelType() != "direct-tcpip" || ssh.Unmarshal(channel.ExtraData(), &payload) != nil || payload.Host != "internal.example.invalid" || deny {
						channel.Reject(ssh.Prohibited, "denied")
						continue
					}
					upstream, err := net.Dial("tcp", destination)
					if err != nil {
						channel.Reject(ssh.ConnectionFailed, "unavailable")
						continue
					}
					forwarded, requests, err := channel.Accept()
					if err != nil {
						upstream.Close()
						continue
					}
					go ssh.DiscardRequests(requests)
					wg.Add(1)
					go func() {
						defer wg.Done()
						defer upstream.Close()
						defer forwarded.Close()
						done := make(chan struct{})
						go func() { io.Copy(upstream, forwarded); upstream.Close(); close(done) }()
						io.Copy(forwarded, upstream)
						forwarded.Close()
						<-done
					}()
				}
			}()
		}
	}()
	return listener.Addr().String(), &active
}

func generatedPrivateKey(t *testing.T) (ssh.Signer, ed25519.PrivateKey) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return signer, private
}

func keyFile(t *testing.T) (string, ssh.Signer) {
	t.Helper()
	// The key is generated at runtime; no fixture credentials are shipped.
	signer, private := generatedPrivateKey(t)
	block, err := ssh.MarshalPrivateKey(private, "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0600); err != nil {
		t.Fatal(err)
	}
	return path, signer
}

func TestHTTPThroughSSHAndCleanup(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "internal.example.invalid:8080" || r.Header.Get("X-Test") != "preserved" {
			t.Error("request changed")
		}
		io.Copy(w, r.Body)
	}))
	defer upstream.Close()
	keyPath, key := keyFile(t)
	address, active := jumpServer(t, key.PublicKey(), strings.TrimPrefix(upstream.URL, "http://"), false)
	host, port, _ := net.SplitHostPort(address)
	var portNumber int
	fmt.Sscan(port, &portNumber)
	cfg := config.SSHTunnel{ID: "test", Host: host, Port: portNumber, User: "hookfly", PrivateKeyFile: keyPath, KnownHostsFile: filepath.Join(t.TempDir(), "known_hosts"), HostKeyPolicy: "accept_new"}
	tunnel, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	client, err := tunnel.HTTPClient("http://internal.example.invalid:8080")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if i == 1 {
			fresh, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			client, err = fresh.HTTPClient("http://internal.example.invalid:8080")
			if err != nil {
				t.Fatal(err)
			}
		}
		request, _ := http.NewRequest("POST", "http://internal.example.invalid:8080/test", strings.NewReader("payload"))
		request.Header.Set("X-Test", "preserved")
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if string(body) != "payload" {
			t.Fatalf("response %q", body)
		}
	}
	deadline := time.Now().Add(time.Second)
	for active.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if active.Load() != 0 {
		t.Fatal("SSH connection leaked after response close")
	}
	if _, err := os.Stat(cfg.KnownHostsFile); err != nil {
		t.Fatal("missing persistent trust")
	}
	request, _ := http.NewRequest("GET", "http://other.example.invalid/", nil)
	if _, err := client.Do(request); err == nil {
		t.Fatal("allowed a different destination")
	}
}

func TestSSHHandshakeCancellation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, err := listener.Accept()
		if err == nil {
			defer c.Close()
			io.Copy(io.Discard, c)
		}
	}()
	keyPath, _ := keyFile(t)
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	var number int
	fmt.Sscan(port, &number)
	tunnel, err := New(config.SSHTunnel{Host: host, Port: number, User: "test", PrivateKeyFile: keyPath, KnownHostsFile: filepath.Join(t.TempDir(), "known_hosts")})
	if err != nil {
		t.Fatal(err)
	}
	client, _ := tunnel.HTTPClient("http://internal.example.invalid")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, "GET", "http://internal.example.invalid", nil)
	start := time.Now()
	if _, err := client.Do(request); err == nil {
		t.Fatal("expected cancelled handshake")
	}
	if time.Since(start) > time.Second {
		t.Fatal("cancellation was not bounded")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handshake socket leaked")
	}
}

func newTestTunnel(t *testing.T, address, keyPath string) *Tunnel {
	t.Helper()
	host, port, _ := net.SplitHostPort(address)
	number, _ := strconv.Atoi(port)
	tunnel, err := New(config.SSHTunnel{Host: host, Port: number, User: "test", PrivateKeyFile: keyPath, KnownHostsFile: filepath.Join(t.TempDir(), "known_hosts")})
	if err != nil {
		t.Fatal(err)
	}
	return tunnel
}

func TestSSHDeniedForwardingDoesNotReachHTTP(t *testing.T) {
	var reached atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Add(1) }))
	defer upstream.Close()
	keyPath, key := keyFile(t)
	address, _ := jumpServer(t, key.PublicKey(), strings.TrimPrefix(upstream.URL, "http://"), true)
	client, _ := newTestTunnel(t, address, keyPath).HTTPClient("http://internal.example.invalid")
	if _, err := client.Get("http://internal.example.invalid"); err == nil {
		t.Fatal("accepted denied forwarding")
	}
	if reached.Load() != 0 {
		t.Fatal("HTTP request bypassed forwarding refusal")
	}
}

func TestSSHRejectsWrongUserKey(t *testing.T) {
	keyPath, _ := keyFile(t)
	address, _ := jumpServer(t, testKey(t).PublicKey(), "", false)
	client, _ := newTestTunnel(t, address, keyPath).HTTPClient("http://internal.example.invalid")
	if _, err := client.Get("http://internal.example.invalid"); err == nil {
		t.Fatal("accepted wrong private key")
	}
}

func TestSSHTLSKeepsServerNameAndCertificateVerification(t *testing.T) {
	names := make(chan string, 1)
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("HTTP request reached untrusted TLS server") }))
	upstream.TLS = &tls.Config{GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) { names <- hello.ServerName; return nil, nil }}
	upstream.Config.ErrorLog = log.New(io.Discard, "", 0)
	upstream.StartTLS()
	defer upstream.Close()
	keyPath, key := keyFile(t)
	address, _ := jumpServer(t, key.PublicKey(), strings.TrimPrefix(upstream.URL, "https://"), false)
	client, _ := newTestTunnel(t, address, keyPath).HTTPClient("https://internal.example.invalid")
	if _, err := client.Get("https://internal.example.invalid"); err == nil {
		t.Fatal("accepted untrusted TLS certificate")
	}
	select {
	case name := <-names:
		if name != "internal.example.invalid" {
			t.Fatalf("SNI %q", name)
		}
	case <-time.After(time.Second):
		t.Fatal("no TLS handshake")
	}
}

func TestSSHDoesNotReplayRequestAfterLostResponse(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		conn, _, _ := w.(http.Hijacker).Hijack()
		conn.Close()
	}))
	defer upstream.Close()
	keyPath, key := keyFile(t)
	address, _ := jumpServer(t, key.PublicKey(), strings.TrimPrefix(upstream.URL, "http://"), false)
	client, _ := newTestTunnel(t, address, keyPath).HTTPClient("http://internal.example.invalid")
	request, _ := http.NewRequest("POST", "http://internal.example.invalid", strings.NewReader("deploy"))
	request.Header.Set("Idempotency-Key", "delivery")
	if _, err := client.Do(request); err == nil {
		t.Fatal("expected missing response error")
	}
	if calls.Load() != 1 {
		t.Fatalf("request sent %d times", calls.Load())
	}
}

func TestSSHUnreadResponseCancellationClosesConnection(t *testing.T) {
	started := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer upstream.Close()
	keyPath, key := keyFile(t)
	address, active := jumpServer(t, key.PublicKey(), strings.TrimPrefix(upstream.URL, "http://"), false)
	client, _ := newTestTunnel(t, address, keyPath).HTTPClient("http://internal.example.invalid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, "GET", "http://internal.example.invalid", nil)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	<-started
	cancel()
	deadline := time.Now().Add(time.Second)
	for active.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if active.Load() != 0 {
		t.Fatal("SSH socket survived cancellation")
	}
}

func TestSSHReadsEncryptedPrivateKey(t *testing.T) {
	_, private := generatedPrivateKey(t)
	block, err := ssh.MarshalPrivateKeyWithPassphrase(private, "", []byte("test-passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "encrypted")
	os.WriteFile(path, pem.EncodeToMemory(block), 0600)
	cfg := config.SSHTunnel{Host: "jump.example.invalid", User: "test", PrivateKeyFile: path, KnownHostsFile: filepath.Join(t.TempDir(), "known_hosts"), PrivateKeyPassphrase: "test-passphrase"}
	if _, err := New(cfg); err != nil {
		t.Fatal(err)
	}
	cfg.PrivateKeyPassphrase = "wrong"
	if _, err := New(cfg); err == nil || strings.Contains(err.Error(), "wrong") {
		t.Fatal("accepted or leaked incorrect passphrase")
	}
}

func TestSSHHostKeyMismatchNeverSendsHTTP(t *testing.T) {
	var reached atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Add(1) }))
	defer upstream.Close()
	keyPath, key := keyFile(t)
	address, _ := jumpServer(t, key.PublicKey(), strings.TrimPrefix(upstream.URL, "http://"), false)
	tunnel := newTestTunnel(t, address, keyPath)
	remote, _ := net.ResolveTCPAddr("tcp", address)
	if err := acceptNew(context.Background(), tunnel.knownHosts, address, remote, testKey(t).PublicKey()); err != nil {
		t.Fatal(err)
	}
	client, _ := tunnel.HTTPClient("http://internal.example.invalid")
	if _, err := client.Get("http://internal.example.invalid"); err == nil {
		t.Fatal("accepted changed server key")
	}
	if reached.Load() != 0 {
		t.Fatal("sent HTTP through untrusted jump host")
	}
}
