package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"

	"gopkg.in/yaml.v3"
)

var exactEnvironmentReference = regexp.MustCompile(`^\$\{([A-Za-z_][A-Za-z0-9_]*)\}$`)

// Decode strictly decodes a discovered configuration set.
func Decode(candidate Candidate, lookup EnvLookup) (*Bundle, error) {
	global, err := decodeGlobal(candidate.Global, lookup)
	if err != nil {
		return nil, err
	}
	bundle := &Bundle{Global: global}
	for _, source := range candidate.Resources {
		if err := decodeResource(bundle, source, lookup); err != nil {
			return nil, err
		}
	}
	return bundle, nil
}

func decodeGlobal(source SourceFile, lookup EnvLookup) (Global, error) {
	node, err := parseDocument(source)
	if err != nil {
		return Global{}, err
	}
	var global Global
	if err := decodeStrict(node, &global); err != nil {
		return Global{}, strictSourceDecodeError(source)
	}
	if global.Kind != "Hookfly" {
		return Global{}, fmt.Errorf("unsupported global configuration %q", source.Path)
	}
	return global, nil
}

func decodeResource(bundle *Bundle, source SourceFile, lookup EnvLookup) error {
	node, err := parseDocument(source)
	if err != nil {
		return err
	}
	var header documentHeader
	if err := node.Decode(&header); err != nil {
		return sourceDecodeError(source, err)
	}
	if err := expandSecretEnvironmentReferences(node, lookup); err != nil {
		return fmt.Errorf("decode configuration %q: %w", source.Path, err)
	}
	switch header.Kind {
	case "GitLabSources":
		var document gitLabSourcesDocument
		if err := decodeStrict(node, &document); err != nil {
			return strictSourceDecodeError(source)
		}
		for _, configured := range document.Sources {
			sourceValue, err := configured.toGitLabSource(source.Path)
			if err != nil {
				return err
			}
			bundle.GitLabSources = append(bundle.GitLabSources, sourceValue)
		}
	case "GitHubSources":
		var document gitHubSourcesDocument
		if err := decodeStrict(node, &document); err != nil {
			return strictSourceDecodeError(source)
		}
		for _, configured := range document.Sources {
			sourceValue, err := configured.toGitHubSource(source.Path)
			if err != nil {
				return err
			}
			bundle.GitHubSources = append(bundle.GitHubSources, sourceValue)
		}
	case "DokployTargets":
		var document dokployTargetsDocument
		if err := decodeStrict(node, &document); err != nil {
			return strictSourceDecodeError(source)
		}
		for index := range document.Connections {
			if document.Connections[index].APIKey == "" {
				return fmt.Errorf("decode configuration %q: connections api_key is required", source.Path)
			}
			document.Connections[index].Location = SourceLocation{Path: source.Path, Field: "connections"}
		}
		for index := range document.Targets {
			document.Targets[index].Location = SourceLocation{Path: source.Path, Field: "targets"}
		}
		bundle.DokployConnections = append(bundle.DokployConnections, document.Connections...)
		bundle.Targets = append(bundle.Targets, document.Targets...)
	case "HTTPConnections":
		var document httpConnectionsDocument
		if err := decodeStrict(node, &document); err != nil {
			return strictSourceDecodeError(source)
		}
		for index := range document.Connections {
			document.Connections[index].Location = SourceLocation{Path: source.Path, Field: "connections"}
		}
		bundle.HTTPConnections = append(bundle.HTTPConnections, document.Connections...)
	case "HTTPTargets":
		var document httpTargetsDocument
		if err := decodeStrict(node, &document); err != nil {
			return strictSourceDecodeError(source)
		}
		for index := range document.Targets {
			document.Targets[index].Location = SourceLocation{Path: source.Path, Field: "targets"}
		}
		bundle.Targets = append(bundle.Targets, document.Targets...)
	case "Routes":
		var document routesDocument
		if err := decodeStrict(node, &document); err != nil {
			return strictSourceDecodeError(source)
		}
		for index := range document.Routes {
			if document.Routes[index].Priority == nil {
				return fmt.Errorf("decode configuration %q: routes priority is required", source.Path)
			}
			document.Routes[index].Location = SourceLocation{Path: source.Path, Field: "routes"}
		}
		bundle.Routes = append(bundle.Routes, document.Routes...)
	default:
		return fmt.Errorf("unsupported configuration resource %q", source.Path)
	}
	return nil
}

func parseDocument(source SourceFile) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(source.Content))
	var node yaml.Node
	if err := decoder.Decode(&node); err != nil {
		return nil, sourceDecodeError(source, err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err == nil {
		return nil, fmt.Errorf("decode configuration %q: multiple YAML documents are not supported", source.Path)
	} else if !errors.Is(err, io.EOF) {
		return nil, sourceDecodeError(source, err)
	}
	if err := rejectDuplicateMappingKeys(&node); err != nil {
		return nil, sourceDecodeError(source, err)
	}
	return &node, nil
}

func decodeStrict(node *yaml.Node, destination any) error {
	encoded, err := yaml.Marshal(node)
	if err != nil {
		return err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(encoded))
	decoder.KnownFields(true)
	return decoder.Decode(destination)
}

func rejectDuplicateMappingKeys(node *yaml.Node) error {
	switch node.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, child := range node.Content {
			if err := rejectDuplicateMappingKeys(child); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		seen := make(map[string]struct{}, len(node.Content)/2)
		for index := 0; index < len(node.Content); index += 2 {
			key := node.Content[index]
			if key.Kind != yaml.ScalarNode {
				return fmt.Errorf("mapping keys must be scalars")
			}
			if _, exists := seen[key.Value]; exists {
				return fmt.Errorf("duplicate mapping key %q", key.Value)
			}
			seen[key.Value] = struct{}{}
			if err := rejectDuplicateMappingKeys(node.Content[index+1]); err != nil {
				return err
			}
		}
	}
	return nil
}

func expandSecretEnvironmentReferences(node *yaml.Node, lookup EnvLookup) error {
	return expandExactEnvironmentReferences(node, lookup, nil)
}

func expandExactEnvironmentReferences(node *yaml.Node, lookup EnvLookup, path []string) error {
	switch node.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, child := range node.Content {
			if err := expandExactEnvironmentReferences(child, lookup, path); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		for index := 0; index < len(node.Content); index += 2 {
			if err := expandExactEnvironmentReferences(node.Content[index+1], lookup, append(path, node.Content[index].Value)); err != nil {
				return err
			}
		}
	case yaml.ScalarNode:
		if !secretEnvironmentPath(path) {
			return nil
		}
		match := exactEnvironmentReference.FindStringSubmatch(node.Value)
		if match == nil {
			return nil
		}
		value, ok := "", false
		if lookup != nil {
			value, ok = lookup(match[1])
		}
		if !ok || value == "" {
			return fmt.Errorf("environment variable %q is missing or empty", match[1])
		}
		node.Value = value
		node.Tag = "!!str"
		node.Style = yaml.DoubleQuotedStyle
	}
	return nil
}

func secretEnvironmentPath(path []string) bool {
	if len(path) == 0 {
		return false
	}
	switch path[len(path)-1] {
	case "token", "secret", "api_key":
		return true
	case "value":
		return len(path) >= 2 && path[len(path)-2] == "auth"
	default:
		return false
	}
}

func sourceDecodeError(source SourceFile, err error) error {
	return fmt.Errorf("decode configuration %q: %w", source.Path, err)
}

func strictSourceDecodeError(source SourceFile) error {
	return fmt.Errorf("decode configuration %q: invalid configuration fields", source.Path)
}

type documentHeader struct {
	Kind string `yaml:"kind"`
}

type gitLabSourcesDocument struct {
	Kind    string                 `yaml:"kind"`
	Sources []gitLabSourceDocument `yaml:"sources"`
}

type gitLabSourceDocument struct {
	ID           string                     `yaml:"id"`
	Token        string                     `yaml:"token"`
	Repositories []gitLabRepositoryDocument `yaml:"repositories"`
}

type gitLabRepositoryDocument struct {
	ID           string `yaml:"id"`
	Name         string `yaml:"name"`
	ProjectID    int64  `yaml:"project_id"`
	HistoryLimit *int   `yaml:"history_limit"`
}

func (document gitLabSourceDocument) toGitLabSource(path string) (GitLabSource, error) {
	if document.Token == "" {
		return GitLabSource{}, fmt.Errorf("decode configuration %q: sources token is required", path)
	}
	source := GitLabSource{ID: document.ID, Token: document.Token, Location: SourceLocation{Path: path, Field: "sources"}}
	for index, configured := range document.Repositories {
		repository, err := configured.toRepository(path, "project_id", index)
		if err != nil {
			return GitLabSource{}, err
		}
		source.Repositories = append(source.Repositories, repository)
	}
	return source, nil
}

func (document gitLabRepositoryDocument) toRepository(path, field string, index int) (Repository, error) {
	if document.ProjectID <= 0 {
		return Repository{}, fmt.Errorf("decode configuration %q: repository %s must be positive", path, field)
	}
	name := document.Name
	if name == "" {
		name = document.ID
	}
	return Repository{
		ID: document.ID, Name: name, ExternalID: strconv.FormatInt(document.ProjectID, 10),
		HistoryLimit: document.HistoryLimit, Location: SourceLocation{Path: path, Field: fmt.Sprintf("repositories[%d]", index)},
	}, nil
}

type gitHubSourcesDocument struct {
	Kind    string                 `yaml:"kind"`
	Sources []gitHubSourceDocument `yaml:"sources"`
}

type gitHubSourceDocument struct {
	ID           string                     `yaml:"id"`
	Secret       string                     `yaml:"secret"`
	Repositories []gitHubRepositoryDocument `yaml:"repositories"`
}

type gitHubRepositoryDocument struct {
	ID           string `yaml:"id"`
	Name         string `yaml:"name"`
	RepositoryID int64  `yaml:"repository_id"`
	HistoryLimit *int   `yaml:"history_limit"`
}

func (document gitHubSourceDocument) toGitHubSource(path string) (GitHubSource, error) {
	if document.Secret == "" {
		return GitHubSource{}, fmt.Errorf("decode configuration %q: sources secret is required", path)
	}
	source := GitHubSource{ID: document.ID, Secret: document.Secret, Location: SourceLocation{Path: path, Field: "sources"}}
	for index, configured := range document.Repositories {
		if configured.RepositoryID <= 0 {
			return GitHubSource{}, fmt.Errorf("decode configuration %q: repository_id must be positive", path)
		}
		name := configured.Name
		if name == "" {
			name = configured.ID
		}
		source.Repositories = append(source.Repositories, Repository{
			ID: configured.ID, Name: name, ExternalID: strconv.FormatInt(configured.RepositoryID, 10),
			HistoryLimit: configured.HistoryLimit, Location: SourceLocation{Path: path, Field: fmt.Sprintf("repositories[%d]", index)},
		})
	}
	return source, nil
}

type dokployTargetsDocument struct {
	Kind        string              `yaml:"kind"`
	Connections []DokployConnection `yaml:"connections"`
	Targets     []Target            `yaml:"targets"`
}

type httpConnectionsDocument struct {
	Kind        string           `yaml:"kind"`
	Connections []HTTPConnection `yaml:"connections"`
}

type httpTargetsDocument struct {
	Kind    string   `yaml:"kind"`
	Targets []Target `yaml:"targets"`
}

type routesDocument struct {
	Kind   string  `yaml:"kind"`
	Routes []Route `yaml:"routes"`
}
