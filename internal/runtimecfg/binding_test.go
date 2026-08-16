package runtimecfg

import (
	"testing"
)

func TestBindingStatusAcceptsV2EquivalentOrigins(t *testing.T) {
	generation, err := Compile(generationBundle("https://dokploy.example.invalid"), discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		snapshot string
	}{
		{
			name: "v2 equivalent origin",
			snapshot: `{"binding_version":2,"id":"production","type":"dokploy","resource_type":"compose","resource_id":"compose-production",` +
				`"connection":{"id":"primary","base_url":"https://DOKPLOY.EXAMPLE.INVALID:443/"}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target, status := generation.ResolveTarget("production", []byte(tt.snapshot))
			if status != BindingCompatible || target == nil || target.ComposeID != "compose-production" {
				t.Fatalf("ResolveTarget() = %#v, %q", target, status)
			}
		})
	}
}

func TestBindingStatusDistinguishesChangedAndUnavailable(t *testing.T) {
	generation, err := Compile(generationBundle("https://dokploy.example.invalid"), discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		targetID string
		snapshot string
		want     BindingStatus
	}{
		{
			name: "resource changed", targetID: "production", want: BindingChanged,
			snapshot: `{"binding_version":2,"id":"production","type":"dokploy","resource_type":"compose","resource_id":"compose-old",` +
				`"connection":{"id":"primary","base_url":"https://dokploy.example.invalid"}}`,
		},
		{
			name: "base URL changed", targetID: "production", want: BindingChanged,
			snapshot: `{"binding_version":2,"id":"production","type":"dokploy","resource_type":"compose","resource_id":"compose-production",` +
				`"connection":{"id":"primary","base_url":"https://other.example.invalid"}}`,
		},
		{name: "target removed", targetID: "missing", snapshot: `{}`, want: BindingUnavailable},
		{name: "snapshot missing", targetID: "production", snapshot: ``, want: BindingUnavailable},
		{name: "snapshot malformed", targetID: "production", snapshot: `{`, want: BindingUnavailable},
		{
			name: "legacy version", targetID: "production", want: BindingUnavailable,
			snapshot: `{"binding_version":1,"id":"production","type":"dokploy","resource_type":"compose","resource_id":"compose-production",` +
				`"connection":{"id":"primary","base_url":"https://dokploy.example.invalid"}}`,
		},
		{
			name: "snapshot target mismatch", targetID: "production", want: BindingUnavailable,
			snapshot: `{"binding_version":2,"id":"other","type":"dokploy","resource_type":"compose","resource_id":"compose-production",` +
				`"connection":{"id":"primary","base_url":"https://dokploy.example.invalid"}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target, status := generation.ResolveTarget(tt.targetID, []byte(tt.snapshot))
			if status != tt.want || target != nil {
				t.Fatalf("ResolveTarget() = %#v, %q; want nil, %q", target, status, tt.want)
			}
		})
	}
}

func TestBindingStatusRejectsVersionlessSnapshotForV2Target(t *testing.T) {
	generation, err := Compile(generationBundle("https://dokploy.example.invalid"), discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	snapshot := []byte(`{"id":"production","type":"dokploy","resource_type":"compose","resource_id":"compose-production",` +
		`"connection":{"id":"primary","base_url":"https://dokploy.example.invalid"}}`)
	if target, status := generation.ResolveTarget("production", snapshot); target != nil || status != BindingUnavailable {
		t.Fatalf("ResolveTarget() = %#v, %q", target, status)
	}
}
