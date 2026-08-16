package config

import "time"

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

type Target struct {
	ID           string         `yaml:"id"`
	Type         string         `yaml:"type"`
	Connection   string         `yaml:"connection"`
	ResourceType string         `yaml:"resource_type"`
	ResourceID   string         `yaml:"resource_id"`
	PollTimeout  *Duration      `yaml:"poll_timeout"`
	Location     SourceLocation `yaml:"-"`
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
	DokployConnections []DokployConnection
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
