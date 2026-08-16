package dokploy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"syscall"
	"time"
)

const (
	requestTimeout        = 10 * time.Second
	maxResponseBody       = 64 << 10
	maxDiscoveryBody      = 1 << 20
	truncatedResponseBody = `{"redacted":true,"truncated":true}`
)

// Client enqueues and observes Compose deployments against one startup-configured connection.
type Client struct {
	baseURL        url.URL
	deployEndpoint string
	apiKey         string
	httpClient     *http.Client
	logger         *slog.Logger
}

func NewClient(baseURL, apiKey string, httpClient *http.Client, logger *slog.Logger) (*Client, error) {
	canonical, err := CanonicalBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	parsed, _ := url.Parse(canonical)
	if apiKey == "" {
		return nil, fmt.Errorf("Dokploy API key is required")
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	clientCopy := *httpClient
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	endpoint := *parsed
	endpoint.Path = "/api/compose.deploy"
	endpoint.RawPath = ""
	endpoint.RawQuery = ""
	endpoint.ForceQuery = false
	endpoint.Fragment = ""
	return &Client{
		baseURL:        *parsed,
		deployEndpoint: endpoint.String(),
		apiKey:         apiKey,
		httpClient:     &clientCopy,
		logger:         logger,
	}, nil
}

// CanonicalBaseURL validates and normalizes a Dokploy origin without exposing it in errors.
func CanonicalBaseURL(baseURL string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid Dokploy base URL")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("unsupported Dokploy URL scheme")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", fmt.Errorf("Dokploy base URL cannot contain user info, query, or fragment")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", fmt.Errorf("Dokploy base URL path must be root")
	}
	hostname := strings.ToLower(parsed.Hostname())
	if hostname == "" {
		return "", fmt.Errorf("invalid Dokploy base URL")
	}
	port := parsed.Port()
	if (parsed.Scheme == "http" && port == "80") || (parsed.Scheme == "https" && port == "443") {
		port = ""
	}
	if port == "" {
		parsed.Host = hostname
	} else {
		parsed.Host = net.JoinHostPort(hostname, port)
	}
	parsed.Path = ""
	parsed.RawPath = ""
	return parsed.String(), nil
}

// Deploy performs exactly one authenticated enqueue request. It never retries.
func (c *Client) Deploy(ctx context.Context, deployRequest DeployRequest) (DeployResponse, error) {
	return c.DeployWithRequestEvidence(ctx, deployRequest, nil)
}

// DeployWithRequestEvidence records credential-free evidence immediately before the transport call.
func (c *Client) DeployWithRequestEvidence(ctx context.Context, deployRequest DeployRequest, record func([]byte) error) (DeployResponse, error) {
	if deployRequest.ComposeID == "" {
		return DeployResponse{}, c.transportError(TransportDefinitive, errors.New("Compose ID is required"))
	}
	if err := ctx.Err(); err != nil {
		return DeployResponse{}, c.transportError(TransportDefinitive, err)
	}
	payload, err := json.Marshal(deployRequest)
	if err != nil {
		return DeployResponse{}, c.transportError(TransportDefinitive, fmt.Errorf("encode request: %w", err))
	}

	requestContext, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	requestBody := io.NopCloser(bytes.NewReader(payload))
	request, err := http.NewRequestWithContext(requestContext, http.MethodPost, c.deployEndpoint, requestBody)
	if err != nil {
		return DeployResponse{}, c.transportError(TransportDefinitive, fmt.Errorf("build request: %w", err))
	}
	request.ContentLength = int64(len(payload))
	request.Header.Set("Content-Type", "application/json")
	if record != nil {
		evidence, err := c.deployRequestEvidence(deployRequest)
		if err != nil {
			return DeployResponse{}, c.transportError(TransportDefinitive, fmt.Errorf("encode request evidence: %w", err))
		}
		if requestContainsSecret(deployRequest, c.apiKey) || bytes.Contains(evidence, []byte(c.apiKey)) {
			evidence = nil
		}
		if err := record(evidence); err != nil {
			return DeployResponse{}, c.transportError(TransportDefinitive, fmt.Errorf("record request evidence: %w", err))
		}
	}
	request.Header.Set("x-api-key", c.apiKey)
	response, err := c.httpClient.Do(request)
	if err != nil {
		kind := classifyTransportError(err)
		transportErr := c.transportError(kind, err)
		c.logger.WarnContext(ctx, "Dokploy Compose enqueue failed", "kind", kind, "error", transportErr)
		return DeployResponse{}, transportErr
	}
	defer response.Body.Close()

	body, truncated, readErr := readBounded(response.Body, maxResponseBody)
	body, redactionTruncated := c.scrubResponseBody(body, truncated || readErr != nil, maxResponseBody)
	result := DeployResponse{
		StatusCode: response.StatusCode,
		Body:       body,
		Truncated:  truncated || redactionTruncated,
	}
	if readErr != nil {
		c.logger.WarnContext(ctx, "Dokploy response body ended early", "status_code", response.StatusCode)
	}
	if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
		c.logger.InfoContext(ctx, "Dokploy Compose enqueue acknowledged", "status_code", response.StatusCode)
		return result, nil
	}
	err = c.transportError(TransportDefinitive, fmt.Errorf("unexpected HTTP status %d", response.StatusCode))
	c.logger.WarnContext(ctx, "Dokploy Compose enqueue rejected", "status_code", response.StatusCode)
	return result, err
}

func requestContainsSecret(request DeployRequest, secret string) bool {
	return strings.Contains(request.ComposeID, secret) || strings.Contains(request.Title, secret) || strings.Contains(request.Description, secret)
}

// FindDeployment streams newest-first Compose history and resumes by deployment ID after the initial attempt match.
func (c *Client) FindDeployment(ctx context.Context, composeID, attemptID, deploymentID string) (Deployment, bool, error) {
	if composeID == "" {
		return Deployment{}, false, errors.New("Compose ID is required")
	}
	if attemptID == "" {
		return Deployment{}, false, errors.New("attempt ID is required")
	}
	var match Deployment
	err := c.walkDeploymentHistory(ctx, composeID, func(deployment Deployment) bool {
		if deploymentMatches(deployment, attemptID, deploymentID) {
			match = deployment
			return false
		}
		return true
	})
	return match, match.DeploymentID != "", err
}

// LatestDeploymentID returns the newest durable history boundary before a send.
func (c *Client) LatestDeploymentID(ctx context.Context, composeID string) (string, bool, error) {
	var deploymentID string
	err := c.walkDeploymentHistory(ctx, composeID, func(deployment Deployment) bool {
		deploymentID = deployment.DeploymentID
		return false
	})
	return deploymentID, deploymentID != "", err
}

// FindDeploymentAfter binds one post-cursor record and rejects ambiguous history.
func (c *Client) FindDeploymentAfter(ctx context.Context, composeID, attemptID, cursor string) (Deployment, DeploymentLookup, error) {
	if attemptID == "" {
		return Deployment{}, DeploymentNotFound, errors.New("attempt ID is required")
	}
	var candidates []Deployment
	cursorFound := cursor == ""
	err := c.walkDeploymentHistory(ctx, composeID, func(deployment Deployment) bool {
		if cursor != "" && deployment.DeploymentID == cursor {
			cursorFound = true
			return false
		}
		candidates = append(candidates, deployment)
		return true
	})
	if err != nil {
		return Deployment{}, DeploymentNotFound, err
	}
	if !cursorFound {
		return Deployment{}, DeploymentCursorMissing, nil
	}
	if len(candidates) == 0 {
		return Deployment{}, DeploymentNotFound, nil
	}
	if len(candidates) == 1 {
		return candidates[0], DeploymentFound, nil
	}
	var exact Deployment
	exactCount := 0
	for _, candidate := range candidates {
		if deploymentMatchesAttempt(candidate, attemptID) {
			exact = candidate
			exactCount++
		}
	}
	if exactCount == 1 {
		return exact, DeploymentFound, nil
	}
	return Deployment{}, DeploymentAmbiguous, nil
}

func (c *Client) walkDeploymentHistory(ctx context.Context, composeID string, visit func(Deployment) bool) error {
	if composeID == "" {
		return errors.New("Compose ID is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	endpoint := c.baseURL
	endpoint.Path = "/api/deployment.allByCompose"
	endpoint.RawPath = ""
	query := make(url.Values)
	query.Set("composeId", composeID)
	endpoint.RawQuery = query.Encode()
	endpoint.ForceQuery = false
	endpoint.Fragment = ""

	requestContext, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return c.safeError("build deployment list request", err)
	}
	request.Header.Set("x-api-key", c.apiKey)
	response, err := c.httpClient.Do(request)
	if err != nil {
		return c.safeError("list Dokploy deployments", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("list Dokploy deployments: unexpected HTTP status %d", response.StatusCode)
	}
	decoder := json.NewDecoder(response.Body)
	token, err := decoder.Token()
	if err != nil {
		return c.safeError("decode Dokploy deployment list", err)
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '[' {
		return errors.New("decode Dokploy deployment list: expected JSON array")
	}
	for decoder.More() {
		var deployment Deployment
		if err := decoder.Decode(&deployment); err != nil {
			return c.safeError("decode Dokploy deployment list", err)
		}
		if deployment.DeploymentID == "" {
			return errors.New("decode Dokploy deployment list: missing deployment ID")
		}
		if !visit(deployment) {
			return nil
		}
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim(']') {
		if err == nil {
			err = errors.New("expected end of JSON array")
		}
		return c.safeError("decode Dokploy deployment list", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			err = errors.New("unexpected trailing JSON value")
		}
		return c.safeError("decode Dokploy deployment list", err)
	}
	return nil
}

// DiscoverComposeResources returns only the non-sensitive Compose inventory from project.all.
func (c *Client) DiscoverComposeResources(ctx context.Context) ([]ComposeResource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	endpoint := c.baseURL
	endpoint.Path = "/api/project.all"
	endpoint.RawPath = ""
	endpoint.RawQuery = ""
	endpoint.ForceQuery = false
	endpoint.Fragment = ""

	requestContext, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, c.safeError("build Dokploy project inventory request", err)
	}
	request.Header.Set("x-api-key", c.apiKey)
	response, err := c.httpClient.Do(request)
	if err != nil {
		if contextErr := requestContext.Err(); contextErr != nil {
			return nil, contextErr
		}
		return nil, c.safeError("list Dokploy projects", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list Dokploy projects: unexpected HTTP status %d", response.StatusCode)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return nil, errors.New("list Dokploy projects: expected application/json response")
	}
	body, truncated, err := readBounded(response.Body, maxDiscoveryBody)
	if err != nil {
		return nil, c.safeError("read Dokploy project inventory", err)
	}
	if truncated {
		return nil, errors.New("read Dokploy project inventory: response body too large")
	}
	resources, err := decodeComposeResources(body)
	if err != nil {
		return nil, errors.New("decode Dokploy project inventory: invalid response")
	}
	return resources, nil
}

// DiscoverComposeContainers returns only safe runtime facts for containers carrying the exact Compose project label.
func (c *Client) DiscoverComposeContainers(ctx context.Context, appName string) ([]ComposeContainer, error) {
	if appName == "" {
		return nil, errors.New("Dokploy App Name is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	endpoint := c.baseURL
	endpoint.Path = "/api/docker.getContainersByAppNameMatch"
	query := make(url.Values)
	query.Set("appName", appName)
	query.Set("appType", "docker-compose")
	endpoint.RawQuery = query.Encode()

	type listedContainer struct {
		ContainerID string `json:"containerId"`
		Name        string `json:"name"`
	}
	var listed []listedContainer
	if err := c.getJSON(ctx, endpoint, &listed, maxDiscoveryBody, "list Dokploy Compose containers"); err != nil {
		return nil, err
	}
	result := make([]ComposeContainer, 0, len(listed))
	for _, container := range listed {
		if container.ContainerID == "" || container.Name == "" {
			return nil, errors.New("decode Dokploy Compose containers: missing container identity")
		}
		configEndpoint := c.baseURL
		configEndpoint.Path = "/api/docker.getConfig"
		configQuery := make(url.Values)
		configQuery.Set("containerId", container.ContainerID)
		configEndpoint.RawQuery = configQuery.Encode()
		var config struct {
			Name  string `json:"Name"`
			State struct {
				Status string `json:"Status"`
				Health *struct {
					Status string `json:"Status"`
				} `json:"Health"`
			} `json:"State"`
			RestartCount int `json:"RestartCount"`
		}
		if err := c.getJSON(ctx, configEndpoint, &config, maxDiscoveryBody, "read Dokploy container runtime"); err != nil {
			return nil, err
		}
		name := strings.TrimPrefix(config.Name, "/")
		if name == "" {
			name = container.Name
		}
		health := ""
		if config.State.Health != nil {
			health = config.State.Health.Status
		}
		result = append(result, ComposeContainer{Name: name, State: config.State.Status, Health: health, RestartCount: config.RestartCount})
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Name < result[right].Name })
	return result, nil
}

// DiscoverComposeAppName resolves the exact Docker Compose project name for one Compose ID.
func (c *Client) DiscoverComposeAppName(ctx context.Context, composeID string) (string, error) {
	if composeID == "" {
		return "", errors.New("Compose ID is required")
	}
	endpoint := c.baseURL
	endpoint.Path = "/api/compose.one"
	query := make(url.Values)
	query.Set("composeId", composeID)
	endpoint.RawQuery = query.Encode()
	var result struct {
		ComposeID string `json:"composeId"`
		AppName   string `json:"appName"`
	}
	if err := c.getJSON(ctx, endpoint, &result, maxDiscoveryBody, "read Dokploy Compose identity"); err != nil {
		return "", err
	}
	if result.ComposeID != composeID || result.AppName == "" {
		return "", errors.New("read Dokploy Compose identity: invalid response")
	}
	return result.AppName, nil
}

func (c *Client) getJSON(ctx context.Context, endpoint url.URL, destination any, limit int, operation string) error {
	requestContext, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return c.safeError("build "+operation+" request", err)
	}
	request.Header.Set("x-api-key", c.apiKey)
	response, err := c.httpClient.Do(request)
	if err != nil {
		if contextErr := requestContext.Err(); contextErr != nil {
			return contextErr
		}
		return c.safeError(operation, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: unexpected HTTP status %d", operation, response.StatusCode)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return fmt.Errorf("%s: expected application/json response", operation)
	}
	body, truncated, err := readBounded(response.Body, limit)
	if err != nil {
		return c.safeError(operation, err)
	}
	if truncated {
		return fmt.Errorf("%s: response body too large", operation)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("%s: invalid response", operation)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("%s: invalid response", operation)
	}
	return nil
}

func decodeComposeResources(body []byte) ([]ComposeResource, error) {
	type composeDocument struct {
		ComposeID     string  `json:"composeId"`
		Name          string  `json:"name"`
		AppName       *string `json:"appName"`
		ComposeStatus *string `json:"composeStatus"`
	}
	type environmentDocument struct {
		Name    string            `json:"name"`
		Compose []composeDocument `json:"compose"`
	}
	type projectDocument struct {
		Name         string                `json:"name"`
		Environments []environmentDocument `json:"environments"`
	}

	trimmed := bytes.TrimSpace(body)
	if len(trimmed) < 2 || trimmed[0] != '[' || trimmed[len(trimmed)-1] != ']' {
		return nil, errors.New("expected JSON array")
	}
	var projects []projectDocument
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	if err := decoder.Decode(&projects); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			err = errors.New("unexpected trailing JSON value")
		}
		return nil, err
	}

	resources := make([]ComposeResource, 0)
	for _, project := range projects {
		for _, environment := range project.Environments {
			for _, compose := range environment.Compose {
				if project.Name == "" || environment.Name == "" || compose.Name == "" || compose.ComposeID == "" {
					return nil, errors.New("missing Compose identity")
				}
				resource := ComposeResource{
					ProjectName: project.Name, EnvironmentName: environment.Name,
					Name: compose.Name, ResourceID: compose.ComposeID,
				}
				if compose.AppName != nil {
					resource.AppName = *compose.AppName
				}
				if compose.ComposeStatus != nil {
					resource.Status = *compose.ComposeStatus
				}
				resources = append(resources, resource)
			}
		}
	}
	sort.Slice(resources, func(left, right int) bool {
		if resources[left].ProjectName != resources[right].ProjectName {
			return resources[left].ProjectName < resources[right].ProjectName
		}
		if resources[left].EnvironmentName != resources[right].EnvironmentName {
			return resources[left].EnvironmentName < resources[right].EnvironmentName
		}
		if resources[left].Name != resources[right].Name {
			return resources[left].Name < resources[right].Name
		}
		return resources[left].ResourceID < resources[right].ResourceID
	})
	return resources, nil
}

func deploymentMatches(deployment Deployment, attemptID, deploymentID string) bool {
	if deploymentID != "" {
		return deployment.DeploymentID == deploymentID
	}
	return deploymentMatchesAttempt(deployment, attemptID)
}

func deploymentMatchesAttempt(deployment Deployment, attemptID string) bool {
	if deployment.Description == nil {
		return false
	}
	token := "attempt_id=" + attemptID
	for _, field := range strings.Fields(*deployment.Description) {
		if field == token {
			return true
		}
	}
	return false
}

func readBounded(reader io.Reader, limit int) ([]byte, bool, error) {
	data, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	if len(data) > limit {
		return data[:limit], true, err
	}
	return data, false, err
}

func (c *Client) scrubResponseBody(input []byte, truncated bool, limit int) ([]byte, bool) {
	if truncated {
		return []byte(truncatedResponseBody), true
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err == nil {
		var extra any
		if err := decoder.Decode(&extra); err == io.EOF {
			if output, err := json.Marshal(scrubSecret(value, c.apiKey)); err == nil {
				if len(output) > limit {
					return []byte(truncatedResponseBody), true
				}
				return output, false
			}
		}
	}

	output := bytes.ReplaceAll(input, []byte(c.apiKey), []byte("[REDACTED]"))
	if len(output) > limit {
		return []byte(truncatedResponseBody), true
	}
	return output, false
}

func scrubSecret(value any, secret string) any {
	switch typed := value.(type) {
	case string:
		return strings.ReplaceAll(typed, secret, "[REDACTED]")
	case json.Number:
		if typed.String() == secret {
			return "[REDACTED]"
		}
		return typed
	case bool:
		if (typed && secret == "true") || (!typed && secret == "false") {
			return "[REDACTED]"
		}
		return typed
	case nil:
		if secret == "null" {
			return "[REDACTED]"
		}
		return nil
	case []any:
		for index := range typed {
			typed[index] = scrubSecret(typed[index], secret)
		}
		return typed
	case map[string]any:
		cleaned := make(map[string]any, len(typed))
		for key, child := range typed {
			cleaned[strings.ReplaceAll(key, secret, "[REDACTED]")] = scrubSecret(child, secret)
		}
		return cleaned
	default:
		return value
	}
}

func (c *Client) transportError(kind TransportErrorKind, err error) *TransportError {
	message := c.scrubError(err).Error()
	return &TransportError{Kind: kind, Err: errors.New(message)}
}

func (c *Client) safeError(action string, err error) error {
	return fmt.Errorf("%s: %w", action, c.scrubError(err))
}

func (c *Client) scrubError(err error) error {
	return errors.New(strings.ReplaceAll(err.Error(), c.apiKey, "[REDACTED]"))
}

func classifyTransportError(err error) TransportErrorKind {
	var dnsError *net.DNSError
	if errors.As(err, &dnsError) {
		return TransportDefinitive
	}
	var operationError *net.OpError
	if errors.As(err, &operationError) && operationError.Op == "dial" {
		return TransportDefinitive
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return TransportDefinitive
	}
	return TransportUncertain
}
