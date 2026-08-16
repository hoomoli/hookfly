package runtimecfg

import (
	"encoding/json"

	"github.com/hoomoli/hookfly/internal/dokploy"
)

// BindingStatus is the safe compatibility result for a historical delivery target.
type BindingStatus string

const (
	BindingCompatible  BindingStatus = "compatible"
	BindingChanged     BindingStatus = "target_changed"
	BindingUnavailable BindingStatus = "target_unavailable"
)

// BindingStatus compares a historical target snapshot with the current generation.
func (g *Generation) BindingStatus(targetID string, snapshot []byte) BindingStatus {
	_, status := g.ResolveTarget(targetID, snapshot)
	return status
}

// ResolveTarget returns executable runtime data only for a compatible historical binding.
func (g *Generation) ResolveTarget(targetID string, snapshot []byte) (*Target, BindingStatus) {
	current, found := g.targets[targetID]
	if !found || len(snapshot) == 0 {
		return nil, BindingUnavailable
	}
	var historical struct {
		BindingVersion *int   `json:"binding_version"`
		ID             string `json:"id"`
		Type           string `json:"type"`
		ResourceType   string `json:"resource_type"`
		ResourceID     string `json:"resource_id"`
		Connection     struct {
			ID      string `json:"id"`
			BaseURL string `json:"base_url"`
		} `json:"connection"`
	}
	if err := json.Unmarshal(snapshot, &historical); err != nil {
		return nil, BindingUnavailable
	}
	if historical.BindingVersion == nil {
		return nil, BindingUnavailable
	}
	version := *historical.BindingVersion
	if version != current.BindingVersion || historical.ID != targetID || historical.Type == "" ||
		historical.ResourceType == "" || historical.ResourceID == "" || historical.Connection.BaseURL == "" {
		return nil, BindingUnavailable
	}
	canonical, err := dokploy.CanonicalBaseURL(historical.Connection.BaseURL)
	if err != nil {
		return nil, BindingUnavailable
	}
	fingerprint := bindingFingerprint(version, historical.Type, historical.ResourceType, canonical, historical.ResourceID)
	if fingerprint != current.Fingerprint {
		return nil, BindingChanged
	}
	return cloneTarget(current), BindingCompatible
}
