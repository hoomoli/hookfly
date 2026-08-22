package runtimecfg

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/hoomoli/hookfly/internal/sshtunnel"
	"net"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/hoomoli/hookfly/internal/config"
)

type sshSnapshot struct {
	ID   string `json:"id"`
	Host string `json:"host"`
	Port int    `json:"port"`
	User string `json:"user"`
}

type sshDigest struct {
	Identity       sshSnapshot `json:"identity"`
	PrivateKeyFile string      `json:"private_key_file"`
	KnownHostsFile string      `json:"known_hosts_file"`
	Policy         string      `json:"host_key_policy"`
}

func tunnelIdentity(bundle *config.Bundle, id string) *sshSnapshot {
	for _, t := range bundle.SSHTunnels {
		if t.ID == id {
			return &sshSnapshot{ID: t.ID, Host: t.Host, Port: t.Port, User: t.User}
		}
	}
	return nil
}

func tunnelBindingFingerprint(base string, tunnel *sshSnapshot) string {
	if tunnel == nil {
		return base
	}
	data, _ := json.Marshal(tunnel)
	return bindingFingerprint(1, "ssh", base, string(data), "")
}

type SSHTunnelSummary struct {
	ID               string     `json:"id"`
	Host             string     `json:"host,omitempty"`
	Port             int        `json:"port,omitempty"`
	User             string     `json:"user,omitempty"`
	Status           string     `json:"status"`
	Error            string     `json:"error,omitempty"`
	LatencyMS        float64    `json:"latency_ms,omitempty"`
	CheckedAt        *time.Time `json:"checked_at,omitempty"`
	DestinationCount int        `json:"destination_count"`
}
type sshRuntime struct {
	mu           sync.Mutex
	summary      SSHTunnelSummary
	tunnel       *sshtunnel.Tunnel
	destinations []string
	checking     bool
}
type unavailableSSHTransport struct{}

func (unavailableSSHTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, &net.OpError{Op: "dial", Net: "ssh", Err: errors.New("SSH tunnel configuration unavailable")}
}
func (g *Generation) SSHTunnels() []SSHTunnelSummary {
	result := []SSHTunnelSummary{}
	for _, r := range g.sshTunnels {
		r.mu.Lock()
		s := r.summary
		s.DestinationCount = len(r.destinations)
		r.mu.Unlock()
		result = append(result, s)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}
func (m *Manager) SSHTunnels() []SSHTunnelSummary {
	m.gate.RLock()
	defer m.gate.RUnlock()
	return m.current.SSHTunnels()
}

var ErrSSHTunnelNotFound = errors.New("SSH tunnel not found")
var ErrSSHCheckInProgress = errors.New("SSH check in progress")

func (m *Manager) CheckSSHTunnel(ctx context.Context, id string) (SSHTunnelSummary, error) {
	m.gate.RLock()
	r := m.current.sshTunnels[id]
	m.gate.RUnlock()
	if r == nil {
		return SSHTunnelSummary{}, ErrSSHTunnelNotFound
	}
	r.mu.Lock()
	if r.checking {
		r.mu.Unlock()
		return SSHTunnelSummary{}, ErrSSHCheckInProgress
	}
	if r.tunnel == nil || r.summary.Status == "configuration_error" {
		s := r.summary
		r.mu.Unlock()
		return s, nil
	}
	r.checking = true
	r.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	start := time.Now()
	err := r.tunnel.Check(ctx, r.destinations)
	at := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checking = false
	r.summary.CheckedAt = &at
	r.summary.LatencyMS = float64(time.Since(start).Microseconds()) / 1000
	if err != nil {
		r.summary.Status = "connection_error"
		r.summary.Error = "connection_failed"
	} else {
		r.summary.Error = ""
		r.summary.Status = "connected"
		if len(r.destinations) == 0 {
			r.summary.Status = "ssh_only"
		}
	}
	s := r.summary
	s.DestinationCount = len(r.destinations)
	return s, nil
}
