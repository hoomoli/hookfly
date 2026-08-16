package runtimecfg

import (
	"context"
	"errors"
	"sort"

	"github.com/hoomoli/hookfly/internal/dokploy"
)

const (
	ConnectionTypeDokploy                 = "dokploy"
	ConnectionStatusConfigured            = "configured"
	ConnectionCapabilityResourceDiscovery = "resource_discovery"
)

var (
	ErrConnectionNotFound              = errors.New("connection not found")
	ErrConnectionCapabilityUnavailable = errors.New("connection capability unavailable")
)

// ConnectionSummary is the secret-free management projection of one configured connection.
type ConnectionSummary struct {
	ID           string   `json:"id"`
	Type         string   `json:"type"`
	Status       string   `json:"status"`
	Capabilities []string `json:"capabilities"`
}

// ConnectionResource is a provider-independent, non-sensitive discovered resource.
type ConnectionResource struct {
	Type            string `json:"type"`
	ProjectName     string `json:"project_name"`
	EnvironmentName string `json:"environment_name"`
	Name            string `json:"name"`
	AppName         string `json:"app_name,omitempty"`
	ResourceID      string `json:"resource_id"`
	Status          string `json:"status,omitempty"`
}

func (c *connection) discoverContainers(ctx context.Context, appName string) ([]dokploy.ComposeContainer, error) {
	if c == nil || c.kind != ConnectionTypeDokploy || c.client == nil {
		return nil, ErrConnectionCapabilityUnavailable
	}
	return c.client.DiscoverComposeContainers(ctx, appName)
}

func (c *connection) discoverComposeAppName(ctx context.Context, composeID string) (string, error) {
	if c == nil || c.kind != ConnectionTypeDokploy || c.client == nil {
		return "", ErrConnectionCapabilityUnavailable
	}
	return c.client.DiscoverComposeAppName(ctx, composeID)
}

type connection struct {
	id     string
	kind   string
	client *dokploy.Client
}

func (c *connection) summary() ConnectionSummary {
	capabilities := make([]string, 0, 1)
	if c != nil && c.kind == ConnectionTypeDokploy && c.client != nil {
		capabilities = append(capabilities, ConnectionCapabilityResourceDiscovery)
	}
	return ConnectionSummary{
		ID: c.id, Type: c.kind, Status: ConnectionStatusConfigured,
		Capabilities: capabilities,
	}
}

func (c *connection) discoverResources(ctx context.Context) ([]ConnectionResource, error) {
	if c == nil || c.kind != ConnectionTypeDokploy || c.client == nil {
		return nil, ErrConnectionCapabilityUnavailable
	}
	discovered, err := c.client.DiscoverComposeResources(ctx)
	if err != nil {
		return nil, err
	}
	resources := make([]ConnectionResource, 0, len(discovered))
	for _, resource := range discovered {
		resources = append(resources, ConnectionResource{
			Type: "compose", ProjectName: resource.ProjectName, EnvironmentName: resource.EnvironmentName,
			Name: resource.Name, AppName: resource.AppName, ResourceID: resource.ResourceID, Status: resource.Status,
		})
	}
	return resources, nil
}

// Connections returns safe summaries in stable ID order.
func (g *Generation) Connections() []ConnectionSummary {
	if g == nil {
		return []ConnectionSummary{}
	}
	summaries := make([]ConnectionSummary, 0, len(g.connections))
	for _, configured := range g.connections {
		summaries = append(summaries, configured.summary())
	}
	sort.Slice(summaries, func(left, right int) bool { return summaries[left].ID < summaries[right].ID })
	return summaries
}

// Connections returns the current generation's safe connection summaries.
func (m *Manager) Connections() []ConnectionSummary {
	m.gate.RLock()
	defer m.gate.RUnlock()
	return m.current.Connections()
}

// DiscoverConnectionResources snapshots one configured connection before upstream I/O.
func (m *Manager) DiscoverConnectionResources(ctx context.Context, id string) ([]ConnectionResource, error) {
	m.gate.RLock()
	var configured *connection
	if m.current != nil {
		configured = m.current.connections[id]
	}
	m.gate.RUnlock()
	if configured == nil {
		return nil, ErrConnectionNotFound
	}
	return configured.discoverResources(ctx)
}
