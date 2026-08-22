package runtimecfg

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/hoomoli/hookfly/internal/config"
	"golang.org/x/crypto/ssh"
)

func sshBundle(t *testing.T) *config.Bundle {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(private, "")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "key")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0600); err != nil {
		t.Fatal(err)
	}
	bundle := generationBundle("http://dokploy.example.invalid")
	bundle.SSHTunnels = []config.SSHTunnel{{ID: "jump", Host: "jump.example.invalid", User: "hookfly", PrivateKeyFile: path, KnownHostsFile: filepath.Join(dir, "known_hosts")}}
	bundle.DokployConnections[0].SSHTunnel = "jump"
	bundle.HTTPConnections = []config.HTTPConnection{{ID: "http", BaseURL: "http://internal.example.invalid", SSHTunnel: "jump", AllowPrivateNetwork: true, Auth: config.HTTPAuthentication{Type: "bearer", Value: "test-secret"}}}
	bundle.Targets = append(bundle.Targets, config.Target{ID: "http", Type: "http", Connection: "http", Method: "POST", Path: "/deploy"}, config.Target{ID: "forward", Type: "forward", URL: "http://internal.example.invalid/hook", SSHTunnel: "jump", AllowPrivateNetwork: true})
	return bundle
}

func TestSSHTunnelBindingAndSafeSummary(t *testing.T) {
	bundle := sshBundle(t)
	initial, err := Compile(bundle, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bundle.SSHTunnels[0].KnownHostsFile); !os.IsNotExist(err) {
		t.Fatal("compile created host trust")
	}
	encoded, _ := json.Marshal(initial.Connections())
	var summaries []map[string]any
	json.Unmarshal(encoded, &summaries)
	for _, summary := range summaries {
		if summary["ssh_tunnel"] != "jump" {
			t.Fatalf("missing tunnel reference: %s", encoded)
		}
	}
	bundle.SSHTunnels[0].Host = "other.example.invalid"
	changed, err := Compile(bundle, nil)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Digest() == initial.Digest() {
		t.Fatal("tunnel omitted from digest")
	}
	for _, target := range initial.Targets() {
		if got := changed.BindingStatus(target.ID, target.Snapshot); got != BindingChanged {
			t.Fatalf("%s: got %s, want target_changed", target.ID, got)
		}
	}
	bundle.SSHTunnels[0].Host = "jump.example.invalid"
	same, err := Compile(bundle, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range initial.Targets() {
		if got := same.BindingStatus(target.ID, target.Snapshot); got != BindingCompatible {
			t.Fatalf("%s: got %s", target.ID, got)
		}
	}
}

func TestSSHCompileRejectsUnreadableKey(t *testing.T) {
	bundle := sshBundle(t)
	bundle.SSHTunnels[0].PrivateKeyFile = "/not-present/key"
	if _, err := Compile(bundle, nil); err == nil {
		t.Fatal("accepted unreadable SSH key")
	}
}

func TestSSHDirectTransitionAndCredentialRotationBindings(t *testing.T) {
	bundle := sshBundle(t)
	tunneled, err := Compile(bundle, nil)
	if err != nil {
		t.Fatal(err)
	}
	fresh := sshBundle(t)
	bundle.SSHTunnels[0].PrivateKeyFile = fresh.SSHTunnels[0].PrivateKeyFile
	rotated, err := Compile(bundle, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range tunneled.Targets() {
		if rotated.BindingStatus(target.ID, target.Snapshot) != BindingCompatible {
			t.Fatalf("key rotation invalidated %s", target.ID)
		}
	}
	bundle.DokployConnections[0].SSHTunnel = ""
	bundle.HTTPConnections[0].SSHTunnel = ""
	bundle.Targets[len(bundle.Targets)-1].SSHTunnel = ""
	direct, err := Compile(bundle, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range tunneled.Targets() {
		if direct.BindingStatus(target.ID, target.Snapshot) != BindingChanged {
			t.Fatalf("direct transition kept %s binding", target.ID)
		}
	}
	for _, target := range direct.Targets() {
		if tunneled.BindingStatus(target.ID, target.Snapshot) != BindingChanged {
			t.Fatalf("SSH transition kept %s binding", target.ID)
		}
	}
}
