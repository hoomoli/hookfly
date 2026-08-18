package runtimecfg

import (
	"encoding/json"

	"github.com/hoomoli/hookfly/internal/dokploy"
	"github.com/hoomoli/hookfly/internal/httptarget"
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
	var versionHeader struct {
		BindingVersion *int `json:"binding_version"`
	}
	if err := json.Unmarshal(snapshot, &versionHeader); err != nil {
		return nil, BindingUnavailable
	}
	if versionHeader.BindingVersion == nil {
		return nil, BindingUnavailable
	}
	var historical targetSnapshot
	if err := json.Unmarshal(snapshot, &historical); err != nil {
		return nil, BindingUnavailable
	}
	version := *versionHeader.BindingVersion
	if version != current.BindingVersion || historical.ID != targetID || historical.Type == "" || historical.Type != current.Type {
		return nil, BindingUnavailable
	}
	var fingerprint string
	switch historical.Type {
	case "dokploy":
		if historical.ResourceType == "" || historical.ResourceID == "" || historical.Connection.BaseURL == "" {
			return nil, BindingUnavailable
		}
		canonical, err := dokploy.CanonicalBaseURL(historical.Connection.BaseURL)
		if err != nil {
			return nil, BindingUnavailable
		}
		fingerprint = bindingFingerprint(version, historical.Type, historical.ResourceType, canonical, historical.ResourceID)
	case "http":
		if historical.HTTP == nil || historical.Connection.ID == "" || historical.Connection.BaseURL == "" {
			return nil, BindingUnavailable
		}
		canonical, err := httptarget.CanonicalBaseURL(historical.Connection.BaseURL)
		if err != nil {
			return nil, BindingUnavailable
		}
		fingerprint = httpBindingFingerprint(version, historical.Connection.ID, canonical, *historical.HTTP)
	case "forward":
		if historical.Forward == nil {
			return nil, BindingUnavailable
		}
		canonical, err := httptarget.CanonicalForwardURL(historical.Forward.URL)
		if err != nil {
			return nil, BindingUnavailable
		}
		forward := *historical.Forward
		forward.URL = canonical
		fingerprint = forwardBindingFingerprint(version, forward)
	default:
		return nil, BindingUnavailable
	}
	if fingerprint != current.Fingerprint {
		return nil, BindingChanged
	}
	return cloneTarget(current), BindingCompatible
}
