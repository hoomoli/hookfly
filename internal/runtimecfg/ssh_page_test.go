package runtimecfg

import (
	"context"
	"github.com/hoomoli/hookfly/internal/httptarget"
	"strings"
	"testing"
)

func TestSSHInvalidKeyAllowsStartup(t *testing.T) {
	b := sshBundle(t)
	b.SSHTunnels[0].PrivateKeyFile = "/missing/key"
	g, err := Compile(b, nil)
	if err != nil {
		t.Fatalf("SSH failure prevented startup: %v", err)
	}
	if len(g.SSHTunnels()) != 1 || g.SSHTunnels()[0].Status != "configuration_error" {
		t.Fatal("missing configuration error")
	}
}
func TestSSHMissingReferenceAllowsStartupWithoutFallback(t *testing.T) {
	b := sshBundle(t)
	b.SSHTunnels = nil
	g, err := Compile(b, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.SSHTunnels()) != 1 || g.SSHTunnels()[0].Error != "missing_tunnel" {
		t.Fatal("missing unavailable tunnel")
	}
	for _, target := range g.Targets() {
		if target.Type == "http" {
			_, err := target.HTTP.Client.Dispatch(context.Background(), target.HTTP.Request, httptarget.Values{}, nil)
			if err == nil {
				t.Fatal("unavailable tunnel did not fail")
			}
		}
	}
}
func TestSSHEmptyInventory(t *testing.T) {
	g, err := Compile(generationBundle("https://dokploy.example.invalid"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if g.SSHTunnels() == nil || len(g.SSHTunnels()) != 0 {
		t.Fatal("expected empty SSH inventory")
	}
}
func TestSSHPrivateNetworkPermissionFailsClosed(t *testing.T) {
	b := sshBundle(t)
	b.HTTPConnections[0].AllowPrivateNetwork = false
	g, err := Compile(b, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range g.Targets() {
		if target.Type == "http" {
			_, err := target.HTTP.Client.Dispatch(context.Background(), target.HTTP.Request, httptarget.Values{}, nil)
			if err == nil || !strings.Contains(err.Error(), "SSH tunnel configuration unavailable") {
				t.Fatalf("lost SSH failure boundary: %v", err)
			}
		}
	}
}
func TestSSHForwardPermissionUsesConfiguredBinding(t *testing.T) {
	b := sshBundle(t)
	b.Targets[len(b.Targets)-1].URL = "http://LOCALHOST:8080/hook"
	b.Targets[len(b.Targets)-1].AllowPrivateNetwork = false
	g, err := Compile(b, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range g.Targets() {
		if target.Type == "forward" {
			_, err := target.Forward.Client.Dispatch(context.Background(), httptarget.ForwardRequest{Method: "POST"}, nil)
			if err == nil || !strings.Contains(err.Error(), "SSH tunnel configuration unavailable") {
				t.Fatalf("permission was not enforced: %v", err)
			}
		}
	}
}
