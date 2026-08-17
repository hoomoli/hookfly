// Package httptarget dispatches bounded, authenticated requests to one configured origin.
package httptarget

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/hoomoli/hookfly/internal/redact"
)

const (
	requestTimeout  = 10 * time.Second
	maxResponseBody = 64 << 10
)

type TransportErrorKind string

const (
	TransportDefinitive TransportErrorKind = "definitive"
	TransportUncertain  TransportErrorKind = "uncertain"
)

// TransportError distinguishes a response rejection from a send that might have reached the upstream.
type TransportError struct {
	Kind TransportErrorKind
	Err  error
}

func (e *TransportError) Error() string { return fmt.Sprintf("HTTP dispatch %s: %v", e.Kind, e.Err) }
func (e *TransportError) Unwrap() error { return e.Err }

// Request is the non-secret action contract compiled from one HTTP target.
type Request struct {
	Method          string
	Path            string
	Query           map[string][]string
	Headers         map[string]string
	Body            *Body
	SuccessStatuses []int
}

type Body struct {
	Type        string
	ContentType string
	Value       any
}

type Authentication struct {
	Type   string
	Value  string
	Header string
}

// Values is the complete template context. It deliberately excludes raw webhook input.
type Values struct {
	EventSource, EventRepository, EventRef, EventRevision, EventStatus string
	EventExternalID, EventTrigger, EventCommitMessage                  string
	AttemptID, DeliveryID, TargetID                                    string
}

// Response contains safe bounded response evidence.
type Response struct {
	StatusCode int
	Body       []byte
	Truncated  bool
	Accepted   bool
}

// Client is immutable for one HTTP connection.
type Client struct {
	baseURL    url.URL
	auth       Authentication
	httpClient *http.Client
}

func NewClient(baseURL, apiKey, apiKeyHeader string, httpClient *http.Client) (*Client, error) {
	return NewAuthenticatedClient(baseURL, Authentication{Type: "api_key", Value: apiKey, Header: apiKeyHeader}, true, httpClient)
}

func NewAuthenticatedClient(baseURL string, auth Authentication, allowPrivateNetwork bool, httpClient *http.Client) (*Client, error) {
	canonical, err := CanonicalBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	parsed, _ := url.Parse(canonical)
	if auth.Value == "" {
		return nil, errors.New("HTTP authentication value is required")
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	clientCopy := *httpClient
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if !allowPrivateNetwork {
		clientCopy.Transport = privateNetworkSafeTransport(httpClient.Transport)
	}
	return &Client{baseURL: *parsed, auth: auth, httpClient: &clientCopy}, nil
}

// Dispatch records a credential-free request envelope immediately before the only remote call.
func (c *Client) Dispatch(ctx context.Context, configured Request, values Values, record func([]byte) error) (Response, error) {
	if c == nil {
		return Response{}, &TransportError{Kind: TransportDefinitive, Err: errors.New("HTTP client is required")}
	}
	payload, contentType, err := renderBody(configured.Body, values)
	if err != nil {
		return Response{}, c.transportError(TransportDefinitive, err)
	}
	endpoint := c.baseURL
	endpoint.Path = configured.Path
	endpoint.RawPath = ""
	query := endpoint.Query()
	for name, configuredValues := range configured.Query {
		for _, value := range configuredValues {
			rendered, err := renderString(value, values)
			if err != nil {
				return Response{}, c.transportError(TransportDefinitive, err)
			}
			query.Add(name, rendered)
		}
	}
	endpoint.RawQuery = query.Encode()
	requestContext, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, configured.Method, endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return Response{}, c.transportError(TransportDefinitive, fmt.Errorf("build request: %w", err))
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	idempotencyKey := values.DeliveryID
	if idempotencyKey == "" {
		idempotencyKey = values.AttemptID
	}
	request.Header.Set("Idempotency-Key", idempotencyKey)
	for name, value := range configured.Headers {
		rendered, err := renderString(value, values)
		if err != nil {
			return Response{}, c.transportError(TransportDefinitive, err)
		}
		if strings.ContainsAny(rendered, "\r\n") {
			return Response{}, c.transportError(TransportDefinitive, fmt.Errorf("invalid HTTP header value for %q", name))
		}
		request.Header.Set(name, rendered)
	}
	if record != nil {
		evidence, err := requestEvidence(request)
		if err != nil {
			return Response{}, c.transportError(TransportDefinitive, fmt.Errorf("encode request evidence: %w", err))
		}
		if err := record(evidence); err != nil {
			return Response{}, c.transportError(TransportDefinitive, fmt.Errorf("record request evidence: %w", err))
		}
	}
	switch c.auth.Type {
	case "bearer":
		request.Header.Set("Authorization", "Bearer "+c.auth.Value)
	case "api_key":
		request.Header.Set(c.auth.Header, c.auth.Value)
	default:
		return Response{}, c.transportError(TransportDefinitive, errors.New("unsupported HTTP authentication type"))
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return Response{}, c.transportError(classifyTransportError(err), err)
	}
	defer response.Body.Close()
	responseBody, truncated, readErr := readBounded(response.Body, maxResponseBody)
	responseBody, redactionTruncated := scrubResponseBody(responseBody, c.auth.Value, truncated || readErr != nil)
	truncated = truncated || readErr != nil || redactionTruncated
	result := Response{StatusCode: response.StatusCode, Body: responseBody, Truncated: truncated, Accepted: accepts(configured.SuccessStatuses, response.StatusCode)}
	if result.Accepted {
		return result, nil
	}
	return result, c.transportError(TransportDefinitive, fmt.Errorf("unexpected HTTP status %d", response.StatusCode))
}

func renderBody(configured *Body, values Values) ([]byte, string, error) {
	if configured == nil {
		return nil, "", nil
	}
	contentType := configured.ContentType
	switch configured.Type {
	case "json":
		body, err := renderValue(configured.Value, values)
		if err != nil {
			return nil, "", err
		}
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, "", fmt.Errorf("encode JSON request: %w", err)
		}
		if contentType == "" {
			contentType = "application/json"
		}
		return payload, contentType, nil
	case "form":
		form, ok := configured.Value.(map[string][]string)
		if !ok {
			return nil, "", errors.New("invalid HTTP form body")
		}
		valuesByName := make(url.Values, len(form))
		for name, configuredValues := range form {
			for _, value := range configuredValues {
				rendered, err := renderString(value, values)
				if err != nil {
					return nil, "", err
				}
				valuesByName.Add(name, rendered)
			}
		}
		if contentType == "" {
			contentType = "application/x-www-form-urlencoded"
		}
		return []byte(valuesByName.Encode()), contentType, nil
	case "raw":
		value, ok := configured.Value.(string)
		if !ok {
			return nil, "", errors.New("invalid HTTP raw body")
		}
		rendered, err := renderString(value, values)
		if err != nil {
			return nil, "", err
		}
		return []byte(rendered), contentType, nil
	default:
		return nil, "", errors.New("unsupported HTTP body type")
	}
}

// CanonicalBaseURL validates and normalizes an HTTP origin without exposing credentials.
func CanonicalBaseURL(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", errors.New("invalid HTTP base URL")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	hostname := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if (parsed.Scheme == "http" && port == "80") || (parsed.Scheme == "https" && port == "443") {
		port = ""
	}
	if port == "" {
		parsed.Host = hostname
	} else {
		parsed.Host = net.JoinHostPort(hostname, port)
	}
	parsed.Path, parsed.RawPath = "", ""
	return parsed.String(), nil
}

func requestEvidence(request *http.Request) ([]byte, error) {
	var body []byte
	if request.Body != nil {
		var err error
		body, err = io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		request.Body = io.NopCloser(bytes.NewReader(body))
	}
	headers := make(map[string]string, len(request.Header))
	for name, values := range request.Header {
		headers[name] = strings.Join(values, ",")
	}
	evidence := map[string]any{"version": 1, "method": request.Method, "url": request.URL.String(), "headers": headers}
	if len(body) > 0 {
		var decoded any
		if err := json.Unmarshal(body, &decoded); err != nil {
			evidence["body"] = string(body)
		} else {
			evidence["body"] = decoded
		}
	}
	return json.Marshal(evidence)
}

func accepts(statuses []int, status int) bool {
	if len(statuses) == 0 {
		return status >= http.StatusOK && status < http.StatusMultipleChoices
	}
	for _, expected := range statuses {
		if status == expected {
			return true
		}
	}
	return false
}

func renderValue(value any, values Values) (any, error) {
	switch typed := value.(type) {
	case nil, bool, int, int64, uint64, float64:
		return typed, nil
	case string:
		return renderString(typed, values)
	case []any:
		output := make([]any, len(typed))
		for index := range typed {
			child, err := renderValue(typed[index], values)
			if err != nil {
				return nil, err
			}
			output[index] = child
		}
		return output, nil
	case map[string]any:
		output := make(map[string]any, len(typed))
		for key, child := range typed {
			rendered, err := renderValue(child, values)
			if err != nil {
				return nil, err
			}
			output[key] = rendered
		}
		return output, nil
	default:
		return nil, fmt.Errorf("unsupported HTTP request body value")
	}
}

func renderString(value string, values Values) (string, error) {
	replacements := map[string]string{
		"event.source": values.EventSource, "event.repository": values.EventRepository,
		"event.ref": values.EventRef, "event.revision": values.EventRevision,
		"event.status": values.EventStatus, "event.external_id": values.EventExternalID,
		"event.trigger": values.EventTrigger, "event.commit_message": values.EventCommitMessage,
		"attempt.id": values.AttemptID, "target.id": values.TargetID,
	}
	value = httpTemplatePattern.ReplaceAllStringFunc(value, func(token string) string {
		matches := httpTemplatePattern.FindStringSubmatch(token)
		return replacements[matches[1]]
	})
	if strings.Contains(value, "{{") || strings.Contains(value, "}}") {
		return "", errors.New("unsupported HTTP template variable")
	}
	return value, nil
}

var httpTemplatePattern = regexp.MustCompile(`\{\{\s*([^{}\s]+)\s*\}\}`)

func scrubResponseBody(input []byte, secret string, truncated bool) ([]byte, bool) {
	if truncated {
		return []byte(`{"redacted":true,"truncated":true}`), true
	}
	return redact.JSONWithSecret(input, secret, maxResponseBody)
}

func privateNetworkSafeTransport(source http.RoundTripper) http.RoundTripper {
	transport, ok := source.(*http.Transport)
	if !ok || transport == nil {
		transport = http.DefaultTransport.(*http.Transport)
	}
	copyTransport := transport.Clone()
	copyTransport.Proxy = nil
	dialer := &net.Dialer{}
	copyTransport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		for _, address := range addresses {
			if !unsafeAddress(address) {
				return dialer.DialContext(ctx, network, net.JoinHostPort(address.String(), port))
			}
		}
		return nil, fmt.Errorf("HTTP target resolved only to private network addresses")
	}
	return copyTransport
}

func unsafeAddress(address netip.Addr) bool {
	return address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsPrivate() || address.IsUnspecified() || address.IsMulticast()
}

func readBounded(reader io.Reader, limit int) ([]byte, bool, error) {
	data, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	if len(data) > limit {
		return data[:limit], true, err
	}
	return data, false, err
}

func (c *Client) transportError(kind TransportErrorKind, err error) *TransportError {
	return &TransportError{Kind: kind, Err: errors.New(strings.ReplaceAll(err.Error(), c.auth.Value, "[REDACTED]"))}
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
