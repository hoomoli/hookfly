package config

import (
	"bytes"
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

// GlobalHistory is the global retention configuration.
type GlobalHistory struct {
	GlobalLimit            int `yaml:"global_limit"`
	DefaultRepositoryLimit int `yaml:"default_repository_limit"`
}

type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalText(text []byte) error {
	value, err := time.ParseDuration(string(text))
	if err != nil {
		return err
	}
	d.Duration = value
	return nil
}

type Polling struct {
	Interval Duration `yaml:"interval"`
	Timeout  Duration `yaml:"timeout"`
}

type DokployConnection struct {
	ID       string         `yaml:"id"`
	BaseURL  string         `yaml:"base_url"`
	APIKey   string         `yaml:"api_key" secret:"true"`
	Location SourceLocation `yaml:"-"`
}

// HTTPConnection is one startup-configured origin and authentication credential.
type HTTPConnection struct {
	ID                  string             `yaml:"id"`
	BaseURL             string             `yaml:"base_url"`
	AllowPrivateNetwork bool               `yaml:"allow_private_network"`
	Auth                HTTPAuthentication `yaml:"auth"`
	Location            SourceLocation     `yaml:"-"`
}

// HTTPAuthentication is the one secret-bearing part of an HTTP connection.
type HTTPAuthentication struct {
	Type   string `yaml:"type"`
	Value  string `yaml:"value" secret:"true"`
	Header string `yaml:"header"`
}

// HTTPStringList accepts either one scalar or a scalar sequence, preserving repeated values.
type HTTPStringList []string

func (values *HTTPStringList) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		if node.Tag == "!!null" {
			return fmt.Errorf("HTTP value must not be null")
		}
		*values = HTTPStringList{node.Value}
		return nil
	case yaml.SequenceNode:
		result := make(HTTPStringList, len(node.Content))
		for index, value := range node.Content {
			if value.Kind != yaml.ScalarNode || value.Tag == "!!null" {
				return fmt.Errorf("HTTP value must be a scalar")
			}
			result[index] = value.Value
		}
		*values = result
		return nil
	default:
		return fmt.Errorf("HTTP value must be a scalar or sequence")
	}
}

type HTTPBody struct {
	Type        string `yaml:"type"`
	ContentType string `yaml:"content_type"`
	Value       any    `yaml:"value"`
}

func (body *HTTPBody) UnmarshalYAML(node *yaml.Node) error {
	type rawBody struct {
		Type        string    `yaml:"type"`
		ContentType string    `yaml:"content_type"`
		Value       yaml.Node `yaml:"value"`
	}
	encoded, err := yaml.Marshal(node)
	if err != nil {
		return err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(encoded))
	decoder.KnownFields(true)
	var raw rawBody
	if err := decoder.Decode(&raw); err != nil {
		return err
	}
	body.Type, body.ContentType = raw.Type, raw.ContentType
	switch raw.Type {
	case "form":
		var form map[string]HTTPStringList
		if err := raw.Value.Decode(&form); err != nil {
			return err
		}
		body.Value = form
	case "raw":
		var value string
		if err := raw.Value.Decode(&value); err != nil {
			return err
		}
		body.Value = value
	default:
		var value any
		if err := raw.Value.Decode(&value); err != nil {
			return err
		}
		body.Value = value
	}
	return nil
}

type Target struct {
	ID              string                    `yaml:"id"`
	Type            string                    `yaml:"type"`
	Connection      string                    `yaml:"connection"`
	ResourceType    string                    `yaml:"resource_type"`
	ResourceID      string                    `yaml:"resource_id"`
	PollTimeout     *Duration                 `yaml:"poll_timeout"`
	Method          string                    `yaml:"method"`
	Path            string                    `yaml:"path"`
	Query           map[string]HTTPStringList `yaml:"query"`
	Headers         map[string]string         `yaml:"headers"`
	Body            *HTTPBody                 `yaml:"body"`
	SuccessStatuses []int                     `yaml:"success_statuses"`
	Location        SourceLocation            `yaml:"-"`
}

// SourceFile is one configuration file selected during discovery.
type SourceFile struct {
	Path    string
	Content []byte
}

// SourceLocation identifies a configuration field without retaining its value.
type SourceLocation struct {
	Path  string
	Field string
}

func (location SourceLocation) String() string {
	if location.Path == "" {
		return location.Field
	}
	if location.Field == "" {
		return location.Path
	}
	return location.Path + ":" + location.Field
}

// Candidate is the bounded, stable configuration-file snapshot passed to Decode.
type Candidate struct {
	Global    SourceFile
	Resources []SourceFile
}

// EnvLookup resolves environment references during decoding.
type EnvLookup func(string) (string, bool)

// Global is the required configuration document.
type Global struct {
	Kind      string        `yaml:"kind"`
	ConfigDir string        `yaml:"config_dir"`
	History   GlobalHistory `yaml:"history"`
	Polling   Polling       `yaml:"polling"`
}

type GitLabSource struct {
	ID           string         `yaml:"id"`
	Token        string         `yaml:"token" secret:"true"`
	Repositories []Repository   `yaml:"repositories"`
	Location     SourceLocation `yaml:"-"`
}

type GitHubSource struct {
	ID           string         `yaml:"id"`
	Secret       string         `yaml:"secret" secret:"true"`
	Repositories []Repository   `yaml:"repositories"`
	Location     SourceLocation `yaml:"-"`
}

type HarborSource struct {
	ID            string         `yaml:"id"`
	Authorization string         `yaml:"authorization" secret:"true"`
	Repositories  []Repository   `yaml:"repositories"`
	Location      SourceLocation `yaml:"-"`
}

// Repository is a provider-independent repository reference after decoding.
type Repository struct {
	ID           string
	Name         string
	ExternalID   string
	HistoryLimit *int
	Location     SourceLocation
}

type Route struct {
	ID       string         `yaml:"id"`
	Priority *int           `yaml:"priority"`
	Match    RouteMatch     `yaml:"match"`
	Action   Action         `yaml:"action"`
	Location SourceLocation `yaml:"-"`
}

// Bundle is the decoded configuration set.
type Bundle struct {
	Global             Global
	GitLabSources      []GitLabSource
	GitHubSources      []GitHubSource
	HarborSources      []HarborSource
	DokployConnections []DokployConnection
	HTTPConnections    []HTTPConnection
	Targets            []Target
	Routes             []Route
}

// RouteMatch is the provider-independent route predicate.
type RouteMatch struct {
	Source     string  `yaml:"source"`
	Repository string  `yaml:"repository"`
	Event      string  `yaml:"event"`
	Ref        *string `yaml:"ref"`
	Status     *string `yaml:"status"`
	Revision   *string `yaml:"revision"`
	ExternalID *string `yaml:"external_id"`
	Trigger    *string `yaml:"trigger"`
}

type Action struct {
	Type    string   `yaml:"type"`
	Targets []string `yaml:"targets"`
}
