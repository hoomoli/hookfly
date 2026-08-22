package runtimecfg

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoomoli/hookfly/internal/dokploy"
	"github.com/hoomoli/hookfly/internal/httptarget"
	"golang.org/x/crypto/ssh"
)

func startRuntimeJump(t *testing.T, keyPath, destination string) string {
	t.Helper()
	keyBytes, _ := os.ReadFile(keyPath)
	signer, err := ssh.ParsePrivateKey(keyBytes)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if !bytes.Equal(signer.PublicKey().Marshal(), key.Marshal()) {
			return nil, os.ErrPermission
		}
		return nil, nil
	}}
	cfg.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var sockets sync.Map
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
				defer raw.Close()
				defer sockets.Delete(raw)
				conn, channels, requests, err := ssh.NewServerConn(raw, cfg)
				if err != nil {
					return
				}
				defer conn.Close()
				go ssh.DiscardRequests(requests)
				for candidate := range channels {
					if candidate.ChannelType() != "direct-tcpip" {
						candidate.Reject(ssh.Prohibited, "denied")
						continue
					}
					upstream, err := net.Dial("tcp", destination)
					if err != nil {
						candidate.Reject(ssh.ConnectionFailed, "unavailable")
						continue
					}
					channel, reqs, err := candidate.Accept()
					if err != nil {
						upstream.Close()
						continue
					}
					go ssh.DiscardRequests(reqs)
					wg.Add(1)
					go func() {
						defer wg.Done()
						defer upstream.Close()
						defer channel.Close()
						done := make(chan struct{})
						go func() { io.Copy(upstream, channel); upstream.Close(); close(done) }()
						io.Copy(channel, upstream)
						channel.Close()
						<-done
					}()
				}
			}()
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		sockets.Range(func(k, v any) bool { k.(net.Conn).Close(); return true })
		wg.Wait()
	})
	return listener.Addr().String()
}

func TestSSHRuntimeRoutesEveryTargetAndDiscovery(t *testing.T) {
	bundle := sshBundle(t)
	received := make(chan string, 8)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		received <- r.URL.Path
		switch r.URL.Path {
		case "/api/project.all":
			if r.Header.Get("x-api-key") != bundle.DokployConnections[0].APIKey {
				t.Error("missing Dokploy auth")
			}
			io.WriteString(w, "[]")
		case "/api/compose.deploy":
			io.WriteString(w, "{}")
		case "/deploy":
			if r.Header.Get("Authorization") != "Bearer test-secret" {
				t.Error("missing HTTP auth")
			}
			w.WriteHeader(204)
		case "/hook":
			body, _ := io.ReadAll(r.Body)
			if string(body) != "original" || r.URL.RawQuery != "a=1" || r.Header.Get("X-Test") != "forwarded" {
				t.Error("forward request changed")
			}
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	}))
	defer upstream.Close()
	address := startRuntimeJump(t, bundle.SSHTunnels[0].PrivateKeyFile, strings.TrimPrefix(upstream.URL, "http://"))
	bundle.SSHTunnels[0].Host, _, _ = net.SplitHostPort(address)
	_, port, _ := net.SplitHostPort(address)
	bundle.SSHTunnels[0].Port, _ = strconv.Atoi(port)
	generation, err := Compile(bundle, nil)
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(generation, nil, Options{})
	if _, err := manager.DiscoverConnectionResources(context.Background(), "primary"); err != nil {
		t.Fatal(err)
	}
	for _, target := range generation.Targets() {
		switch target.Type {
		case "dokploy":
			if _, err := target.Client.Deploy(context.Background(), dokploy.DeployRequest{ComposeID: target.ComposeID}); err != nil {
				t.Fatal(err)
			}
		case "http":
			if _, err := target.HTTP.Client.Dispatch(context.Background(), target.HTTP.Request, httptarget.Values{DeliveryID: "delivery"}, nil); err != nil {
				t.Fatal(err)
			}
		case "forward":
			if _, err := target.Forward.Client.Dispatch(context.Background(), httptarget.ForwardRequest{Method: "POST", RawQuery: "a=1", Headers: http.Header{"X-Test": []string{"forwarded"}}, Body: []byte("original")}, nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(received) != 4 {
		t.Fatalf("received %d tunneled requests", len(received))
	}
}

func TestSSHOldDiscoveryCompletesAfterGenerationReplacement(t *testing.T) {
	bundle := sshBundle(t)
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		close(started)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		io.WriteString(w, "[]")
	}))
	// Cleanup cancels blocked discovery before closing the test HTTP server.
	ctx, cancel := context.WithCancel(context.Background())
	defer upstream.Close()
	defer cancel()
	address := startRuntimeJump(t, bundle.SSHTunnels[0].PrivateKeyFile, strings.TrimPrefix(upstream.URL, "http://"))
	host, port, _ := net.SplitHostPort(address)
	bundle.SSHTunnels[0].Host = host
	bundle.SSHTunnels[0].Port, _ = strconv.Atoi(port)
	old, err := Compile(bundle, nil)
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(old, nil, Options{})
	done := make(chan error, 1)
	go func() { _, err := manager.DiscoverConnectionResources(ctx, "primary"); done <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("discovery did not start")
	}
	bundle.SSHTunnels[0].Host = "unreachable.example.invalid"
	replacement, err := Compile(bundle, nil)
	if err != nil {
		t.Fatal(err)
	}
	manager.gate.Lock()
	manager.current = replacement
	manager.gate.Unlock()
	release <- struct{}{}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("old discovery interrupted or leaked")
	}
}

func TestSSHManagementCheckVerifiesForwardingWithoutDeploy(t *testing.T) {
	b := sshBundle(t)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer upstream.Close()
	address := startRuntimeJump(t, b.SSHTunnels[0].PrivateKeyFile, strings.TrimPrefix(upstream.URL, "http://"))
	host, port, _ := net.SplitHostPort(address)
	b.SSHTunnels[0].Host = host
	b.SSHTunnels[0].Port, _ = strconv.Atoi(port)
	g, err := Compile(b, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(g, nil, Options{})
	result, err := m.CheckSSHTunnel(context.Background(), "jump")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "connected" || result.CheckedAt == nil || result.LatencyMS <= 0 || result.DestinationCount == 0 {
		t.Fatalf("check result %#v", result)
	}
	if calls.Load() != 0 {
		t.Fatal("check dispatched an HTTP or deployment request")
	}
	if m.SSHTunnels()[0].Status != "connected" {
		t.Fatal("check result was not retained")
	}
}
