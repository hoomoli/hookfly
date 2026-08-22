package config

import (
	"testing"
)

func TestDecodeSSHTunnel(t *testing.T) {
	bundle, err := Decode(Candidate{Global: SourceFile{Path: "hookfly.yaml", Content: []byte("kind: Hookfly\n")}, Resources: []SourceFile{{Path: "ssh.yaml", Content: []byte(`kind: SSHTunnels
tunnels:
  - id: internal
    host: bastion.example.invalid
    user: hookfly
    private_key_file: /run/secrets/ssh_key
    known_hosts_file: /var/lib/hookfly/ssh/known_hosts
    private_key_passphrase: ${SSH_PASSPHRASE}
`)}}}, func(string) (string, bool) { return "test-passphrase", true })
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateAndCanonicalize(bundle); err != nil {
		t.Fatal(err)
	}
}

func TestSSHTunnelValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Bundle)
	}{
		{"unknown tunnel", func(b *Bundle) { b.HTTPConnections[0].SSHTunnel = "missing" }},
		{"private network not allowed", func(b *Bundle) { b.HTTPConnections[0].AllowPrivateNetwork = false }},
		{"insecure policy", func(b *Bundle) { b.SSHTunnels[0].HostKeyPolicy = "insecure" }},
		{"invalid port", func(b *Bundle) { b.SSHTunnels[0].Port = 65536 }},
		{"duplicate", func(b *Bundle) { b.SSHTunnels = append(b.SSHTunnels, b.SSHTunnels[0]) }},
		{"relative key", func(b *Bundle) { b.SSHTunnels[0].PrivateKeyFile = "key" }},
		{"same key and trust file", func(b *Bundle) { b.SSHTunnels[0].KnownHostsFile = b.SSHTunnels[0].PrivateKeyFile }},
		{"target level tunnel", func(b *Bundle) {
			b.Targets = []Target{{ID: "http", Type: "http", Connection: "http", SSHTunnel: "jump", Method: "GET", Path: "/"}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &Bundle{SSHTunnels: []SSHTunnel{{ID: "jump", Host: "jump.example.invalid", User: "hookfly", PrivateKeyFile: "/keys/key", KnownHostsFile: "/data/known_hosts"}}, HTTPConnections: []HTTPConnection{{ID: "http", SSHTunnel: "jump", BaseURL: "http://internal.example.invalid", AllowPrivateNetwork: true, Auth: HTTPAuthentication{Type: "bearer", Value: "secret"}}}}
			tc.mutate(b)
			if err := ValidateAndCanonicalize(b); err == nil {
				t.Fatal("accepted invalid tunnel configuration")
			}
		})
	}
}
