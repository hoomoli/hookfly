package config

import (
	"fmt"
	"net"
	"path/filepath"
	"sort"
	"strings"
)

func validateSSHTunnels(bundle *Bundle) error {
	known := map[string]SourceLocation{}
	for i := range bundle.SSHTunnels {
		tunnel := &bundle.SSHTunnels[i]
		if err := validateIdentifier("SSH tunnel", tunnel.ID); err != nil {
			return err
		}
		if err := addUniqueLocation(known, "SSH tunnel", tunnel.ID, tunnel.Location); err != nil {
			return err
		}
		tunnel.Host = strings.ToLower(tunnel.Host)
		if tunnel.Host == "" || strings.ContainsAny(tunnel.Host, " /\\\t\r\n[]") || (strings.Contains(tunnel.Host, ":") && net.ParseIP(tunnel.Host) == nil) {
			return fmt.Errorf("SSH tunnel %q has invalid host", tunnel.ID)
		}
		if tunnel.Port == 0 {
			tunnel.Port = 22
		}
		if tunnel.Port < 1 || tunnel.Port > 65535 {
			return fmt.Errorf("SSH tunnel %q has invalid port", tunnel.ID)
		}
		if strings.TrimSpace(tunnel.User) == "" || strings.ContainsAny(tunnel.User, "\r\n\x00") {
			return fmt.Errorf("SSH tunnel %q requires user", tunnel.ID)
		}
		if !filepath.IsAbs(tunnel.PrivateKeyFile) || !filepath.IsAbs(tunnel.KnownHostsFile) || filepath.Clean(tunnel.PrivateKeyFile) == filepath.Clean(tunnel.KnownHostsFile) {
			return fmt.Errorf("SSH tunnel %q requires distinct absolute key and trust paths", tunnel.ID)
		}
		if tunnel.HostKeyPolicy == "" {
			tunnel.HostKeyPolicy = "accept_new"
		}
		if tunnel.HostKeyPolicy != "accept_new" {
			return fmt.Errorf("SSH tunnel %q supports only accept_new", tunnel.ID)
		}
	}
	check := func(id string, allowed bool) error {
		if id == "" {
			return nil
		}
		if _, ok := known[id]; !ok {
			return fmt.Errorf("unknown SSH tunnel %q", id)
		}
		if !allowed {
			return fmt.Errorf("SSH tunnel requires allow_private_network")
		}
		return nil
	}
	for _, c := range bundle.HTTPConnections {
		if err := check(c.SSHTunnel, c.AllowPrivateNetwork); err != nil {
			return err
		}
	}
	for _, c := range bundle.DokployConnections {
		if err := check(c.SSHTunnel, true); err != nil {
			return err
		}
	}
	for _, target := range bundle.Targets {
		if target.SSHTunnel != "" && target.Type != "forward" {
			return fmt.Errorf("SSH tunnel belongs on the connection for %q targets", target.Type)
		}
		if err := check(target.SSHTunnel, target.AllowPrivateNetwork); err != nil {
			return err
		}
	}
	sort.Slice(bundle.SSHTunnels, func(i, j int) bool { return bundle.SSHTunnels[i].ID < bundle.SSHTunnels[j].ID })
	return nil
}
