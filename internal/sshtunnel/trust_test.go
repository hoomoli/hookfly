package sshtunnel

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"golang.org/x/crypto/ssh/knownhosts"
	"golang.org/x/sys/unix"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func testKey(t *testing.T) ssh.Signer {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func TestAcceptNewPersistsAndRejectsChangedKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ssh", "known_hosts")
	key := testKey(t).PublicKey()
	addr := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 2222}
	for i := 0; i < 2; i++ {
		if err := acceptNew(context.Background(), path, "jump.example.invalid:2222", addr, key); err != nil {
			t.Fatal(err)
		}
	}
	before, err := os.ReadFile(path)
	if err != nil || len(before) == 0 {
		t.Fatalf("trust was not persisted: %v", err)
	}
	if err := acceptNew(context.Background(), path, "jump.example.invalid:2222", addr, testKey(t).PublicKey()); err == nil {
		t.Fatal("accepted changed host key")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("changed trust record")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("unsafe trust permissions")
	}
}

func TestAcceptNewSerializesConflictingFirstUse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	keys := []ssh.PublicKey{testKey(t).PublicKey(), testKey(t).PublicKey()}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, key := range keys {
		wg.Add(1)
		go func(k ssh.PublicKey) {
			defer wg.Done()
			results <- acceptNew(context.Background(), path, "jump.example.invalid:22", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 22}, k)
		}(key)
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("accepted %d conflicting keys", success)
	}
}

func TestAcceptNewRejectsMalformedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	os.WriteFile(path, []byte("broken record\n"), 0600)
	if err := acceptNew(context.Background(), path, "jump.example.invalid:22", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 22}, testKey(t).PublicKey()); err == nil {
		t.Fatal("accepted malformed trust file")
	}
}

func TestAcceptNewRejectsDifferentAlgorithmForKnownHost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	addr := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 22}
	if err := acceptNew(context.Background(), path, "jump.example.invalid:22", addr, testKey(t).PublicKey()); err != nil {
		t.Fatal(err)
	}
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	other, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	if err := acceptNew(context.Background(), path, "jump.example.invalid:22", addr, other.PublicKey()); err == nil {
		t.Fatal("learned new algorithm for an already trusted host")
	}
}

func TestAcceptNewLockWaitHonorsCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := acceptNew(ctx, path, "jump.example.invalid:22", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 22}, testKey(t).PublicKey()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected cancelled lock wait, got %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("cancelled operation wrote trust file")
	}
}

func TestAcceptNewRejectsRevokedKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	key := testKey(t).PublicKey()
	os.WriteFile(path, []byte("@revoked "+knownhosts.Line([]string{"jump.example.invalid"}, key)+"\n"), 0600)
	if err := acceptNew(context.Background(), path, "jump.example.invalid:22", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 22}, key); err == nil {
		t.Fatal("accepted revoked key")
	}
}

func TestAcceptNewRequiresWritablePersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "known_hosts")
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0700)
	if err := acceptNew(context.Background(), path, "jump.example.invalid:22", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 22}, testKey(t).PublicKey()); err == nil {
		t.Fatal("accepted host without writable trust storage")
	}
}
