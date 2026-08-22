package runtimecfg

import (
	"encoding/json"

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
