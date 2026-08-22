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
			err := ValidateAndCanonicalize(b)
			if tc.name == "target level tunnel" {
				if err == nil {
					t.Fatal("accepted invalid target fields")
				}
				return
			}
			if err != nil {
				t.Fatalf("SSH error prevented startup: %v", err)
			}
			if tc.name != "unknown tunnel" && tc.name != "private network not allowed" && b.SSHTunnels[0].ConfigurationError == "" {
				t.Fatal("missing SSH diagnostic")
			}
		})
	}
}

func TestDecodeMalformedSSHDocumentDoesNotBlockStartup(t *testing.T) {
	for _, raw := range []string{"kind: SSHTunnels\ntunnels: [broken", "kind: SSHTunnels\ntunnels:\n  - id: jump\n    port: invalid\n"} {
		b, err := Decode(Candidate{Global: source("hookfly.yaml", "kind: Hookfly\n"), Resources: []SourceFile{source("ssh.yaml", raw)}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateAndCanonicalize(b); err != nil {
			t.Fatal(err)
		}
		if len(b.SSHTunnels) != 1 || b.SSHTunnels[0].ConfigurationError == "" {
			t.Fatal("missing invalid configuration diagnostic")
		}
	}
}

func TestSSHExceptionDoesNotSwallowOtherDocuments(t *testing.T) {
	_, err := Decode(Candidate{Global: source("hookfly.yaml", "kind: Hookfly\n"), Resources: []SourceFile{source("routes.yaml", "kind: Routes\nroutes: []\n---\nkind: SSHTunnels\ntunnels: []\n")}}, nil)
	if err == nil {
		t.Fatal("SSH exception swallowed invalid Routes document")
	}
}
func TestSSHUnknownSecretFieldIsNonFatal(t *testing.T) {
	b, err := Decode(Candidate{Global: source("hookfly.yaml", "kind: Hookfly\n"), Resources: []SourceFile{source("ssh.yaml", "kind: SSHTunnels\ntunnels:\n  - id: jump\n    token: ${UNSET}\n")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateAndCanonicalize(b); err != nil {
		t.Fatal(err)
	}
	if len(b.SSHTunnels) != 1 || b.SSHTunnels[0].ConfigurationError == "" {
		t.Fatal("missing malformed SSH diagnostic")
	}
}
