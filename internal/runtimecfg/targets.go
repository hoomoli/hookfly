package runtimecfg

import (
	"context"
	"sort"
	"time"
)

type TargetCondition string

const (
	TargetConditionAvailable     TargetCondition = "available"
	TargetConditionUnavailable   TargetCondition = "unavailable"
	TargetConditionRefreshFailed TargetCondition = "refresh_failed"
)

type TargetInventoryItem struct {
	ID              string              `json:"id"`
	ConnectionID    string              `json:"connection_id"`
	ResourceType    string              `json:"resource_type"`
	ResourceID      string              `json:"resource_id"`
	ProjectName     string              `json:"project_name,omitempty"`
	EnvironmentName string              `json:"environment_name,omitempty"`
	Name            string              `json:"name,omitempty"`
	AppName         string              `json:"app_name,omitempty"`
	Status          string              `json:"status,omitempty"`
	Condition       TargetCondition     `json:"condition"`
	RuntimeStatus   TargetRuntimeStatus `json:"runtime_status,omitempty"`
	Containers      []TargetContainer   `json:"containers"`
}

type TargetRuntimeStatus string

const (
	TargetRuntimeAllRunning TargetRuntimeStatus = "all_running"
	TargetRuntimeDegraded   TargetRuntimeStatus = "degraded"
	TargetRuntimeStopped    TargetRuntimeStatus = "stopped"
	TargetRuntimeEmpty      TargetRuntimeStatus = "empty"
	TargetRuntimeUnknown    TargetRuntimeStatus = "unknown"
)

type TargetContainer struct {
	Name         string `json:"name"`
	State        string `json:"state"`
	Health       string `json:"health,omitempty"`
	RestartCount int    `json:"restart_count"`
}

type TargetInventoryError struct {
	ConnectionID string `json:"connection_id"`
	Code         string `json:"code"`
}

type TargetInventory struct {
	Targets     []TargetInventoryItem  `json:"targets"`
	RefreshedAt time.Time              `json:"refreshed_at"`
	Errors      []TargetInventoryError `json:"errors"`
}

type targetRuntimeSnapshot struct {
	appName    string
	status     TargetRuntimeStatus
	containers []TargetContainer
}

func (m *Manager) TargetInventory(ctx context.Context) TargetInventory {
	m.gate.RLock()
	targets := []*Target{}
	connections := map[string]*connection{}
	if m.current != nil {
		targets = m.current.Targets()
		for _, target := range targets {
			connections[target.ConnectionID] = m.current.connections[target.ConnectionID]
		}
	}
	clock := m.clock
	m.gate.RUnlock()

	now := time.Now().UTC()
	if clock != nil {
		now = clock().UTC()
	}
	result := TargetInventory{Targets: []TargetInventoryItem{}, RefreshedAt: now, Errors: []TargetInventoryError{}}
	resources := make(map[string]map[string]ConnectionResource, len(connections))
	runtimes := make(map[string]targetRuntimeSnapshot)
	failed := make(map[string]bool, len(connections))
	connectionIDs := make([]string, 0, len(connections))
	for id := range connections {
		connectionIDs = append(connectionIDs, id)
	}
	sort.Strings(connectionIDs)
	for _, id := range connectionIDs {
		if connections[id] == nil || connections[id].kind != ConnectionTypeDokploy {
			continue
		}
		discovered, err := connections[id].discoverResources(ctx)
		if err != nil {
			failed[id] = true
			result.Errors = append(result.Errors, TargetInventoryError{ConnectionID: id, Code: "upstream_unavailable"})
			continue
		}
		resources[id] = make(map[string]ConnectionResource, len(discovered))
		for _, resource := range discovered {
			resources[id][resource.Type+"\x00"+resource.ResourceID] = resource
		}
	}
	for _, target := range targets {
		item := TargetInventoryItem{ID: target.ID, ConnectionID: target.ConnectionID, ResourceType: target.ResourceType, ResourceID: target.ComposeID, Condition: TargetConditionUnavailable}
		item.Containers = []TargetContainer{}
		if target.Type == ConnectionTypeHTTP {
			item.Name = target.ID
			item.Condition = TargetConditionAvailable
			result.Targets = append(result.Targets, item)
			continue
		}
		if failed[target.ConnectionID] {
			item.Condition = TargetConditionRefreshFailed
		} else if resource, found := resources[target.ConnectionID][target.ResourceType+"\x00"+target.ComposeID]; found {
			item.ProjectName = resource.ProjectName
			item.EnvironmentName = resource.EnvironmentName
			item.Name = resource.Name
			item.AppName = resource.AppName
			item.Status = resource.Status
			item.Condition = TargetConditionAvailable
			runtimeKey := target.ConnectionID + "\x00" + target.ResourceType + "\x00" + target.ComposeID
			runtime, cached := runtimes[runtimeKey]
			if !cached {
				runtime = targetRuntimeSnapshot{status: TargetRuntimeUnknown, containers: []TargetContainer{}}
				if appName, err := connections[target.ConnectionID].discoverComposeAppName(ctx, target.ComposeID); err == nil {
					runtime.appName = appName
					if containers, err := connections[target.ConnectionID].discoverContainers(ctx, appName); err == nil {
						for _, container := range containers {
							runtime.containers = append(runtime.containers, TargetContainer{Name: container.Name, State: container.State, Health: container.Health, RestartCount: container.RestartCount})
						}
						runtime.status = summarizeTargetRuntime(runtime.containers)
					}
				}
				runtimes[runtimeKey] = runtime
			}
			if runtime.appName != "" {
				item.AppName = runtime.appName
			}
			item.RuntimeStatus = runtime.status
			item.Containers = append(item.Containers, runtime.containers...)
		}
		result.Targets = append(result.Targets, item)
	}
	return result
}

func summarizeTargetRuntime(containers []TargetContainer) TargetRuntimeStatus {
	if len(containers) == 0 {
		return TargetRuntimeEmpty
	}
	running := 0
	transitional := 0
	for _, container := range containers {
		if container.State == "running" && container.Health != "unhealthy" {
			running++
		} else if container.State == "created" || container.State == "restarting" {
			transitional++
		}
	}
	if running == len(containers) {
		return TargetRuntimeAllRunning
	}
	if running > 0 || transitional > 0 {
		return TargetRuntimeDegraded
	}
	return TargetRuntimeStopped
}
