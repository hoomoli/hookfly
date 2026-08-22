package config

import (
	"fmt"
	"net"
	"path/filepath"
	"sort"
	"strings"
)

func validateSSHTunnels(bundle *Bundle) error {
	known := map[string]bool{}
	for i := range bundle.SSHTunnels {
		t := &bundle.SSHTunnels[i]
		if validateIdentifier("SSH tunnel", t.ID) != nil {
			t.ID = fmt.Sprintf("invalid-ssh-%d", i+1)
			t.ConfigurationError = "invalid_configuration"
		}
		if known[t.ID] {
			t.ConfigurationError = "duplicate_id"
			for j := 0; j < i; j++ {
				if bundle.SSHTunnels[j].ID == t.ID {
					bundle.SSHTunnels[j].ConfigurationError = "duplicate_id"
				}
			}
		}
		known[t.ID] = true
		t.Host = strings.ToLower(t.Host)
		if t.Port == 0 {
			t.Port = 22
		}
		if t.HostKeyPolicy == "" {
			t.HostKeyPolicy = "accept_new"
		}
		if t.Host == "" || strings.ContainsAny(t.Host, " /\\\t\r\n[]\x00") || (strings.Contains(t.Host, ":") && net.ParseIP(t.Host) == nil) || t.Port < 1 || t.Port > 65535 || strings.TrimSpace(t.User) == "" || strings.ContainsAny(t.User, "\r\n\x00") || !filepath.IsAbs(t.PrivateKeyFile) || !filepath.IsAbs(t.KnownHostsFile) || filepath.Clean(t.PrivateKeyFile) == filepath.Clean(t.KnownHostsFile) || t.HostKeyPolicy != "accept_new" {
			t.ConfigurationError = "invalid_configuration"
		}
	}
	for _, target := range bundle.Targets {
		if target.SSHTunnel != "" && target.Type != "forward" {
			return fmt.Errorf("SSH tunnel belongs on the connection for %q targets", target.Type)
		}
	}
	sort.Slice(bundle.SSHTunnels, func(i, j int) bool { return bundle.SSHTunnels[i].ID < bundle.SSHTunnels[j].ID })
	return nil
}
