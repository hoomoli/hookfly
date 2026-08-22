// Package sshtunnel provides request-scoped SSH forwarding for HTTP clients.
package sshtunnel

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
	"golang.org/x/sys/unix"
)

var errTrust = errors.New("SSH host trust verification failed")

// acceptNew serializes first-use updates across processes and reload generations.
// The separate lock file is stable across atomic replacement of the trust file.
func acceptNew(ctx context.Context, path, hostname string, remote net.Addr, key ssh.PublicKey) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return errTrust
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return errTrust
	}
	defer lock.Close()
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return errTrust
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return errTrust
	}
	if err == nil {
		callback, err := knownhosts.New(path)
		if err != nil {
			return errTrust
		}
		err = callback(hostname, remote, key)
		if err == nil {
			return nil
		}
		var unknown *knownhosts.KeyError
		if !errors.As(err, &unknown) || len(unknown.Want) != 0 {
			return errTrust
		}
	}
	// Certificates must be trusted through a configured authority, not learned.
	if _, ok := key.(*ssh.Certificate); ok {
		return errTrust
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	data = append(data, []byte(knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key)+"\n")...)
	temp, err := os.CreateTemp(filepath.Dir(path), ".known-hosts-*")
	if err != nil {
		return errTrust
	}
	name := temp.Name()
	defer os.Remove(name)
	if _, err = temp.Write(data); err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if err != nil || closeErr != nil {
		return errTrust
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err = os.Rename(name, path); err != nil {
		return errTrust
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return errTrust
	}
	defer directory.Close()
	if err = directory.Sync(); err != nil {
		return errTrust
	}
	return nil
}
