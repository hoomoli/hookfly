package httptarget

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
)

const (
	ForwardHostTarget = "target"
	ForwardHostOrigin = "origin"
)

// ForwardRequest is the durable inbound webhook envelope required for replay.
type ForwardRequest struct {
	Version  int         `json:"version"`
	Method   string      `json:"method"`
	RawQuery string      `json:"raw_query,omitempty"`
	Host     string      `json:"host"`
	Headers  http.Header `json:"headers"`
	Body     []byte      `json:"-"`
}

// ForwardClient replaces only a webhook destination while retaining its request semantics.
type ForwardClient struct {
	destination url.URL
	hostMode    string
	httpClient  *http.Client
}

func NewForwardClient(destination, hostMode string, allowPrivateNetwork bool, httpClient *http.Client) (*ForwardClient, error) {
	parsed, err := canonicalForwardURL(destination)
	if err != nil {
		return nil, err
	}
	if hostMode == "" {
		hostMode = ForwardHostTarget
	}
	if hostMode != ForwardHostTarget && hostMode != ForwardHostOrigin {
		return nil, fmt.Errorf("unsupported forward host mode %q", hostMode)
	}
	if !allowPrivateNetwork && unsafeForwardHost(parsed.Hostname()) {
		return nil, errors.New("private forward URL requires allow_private_network")
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	clientCopy := *httpClient
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if !allowPrivateNetwork {
		safeTransport := privateNetworkSafeTransport(httpClient.Transport)
		if transport, ok := safeTransport.(*http.Transport); ok {
			transport.DisableCompression = true
		}
		clientCopy.Transport = safeTransport
	} else if transport, ok := forwardTransport(httpClient.Transport); ok {
		transport.DisableCompression = true
		clientCopy.Transport = transport
	}
	return &ForwardClient{destination: *parsed, hostMode: hostMode, httpClient: &clientCopy}, nil
}

func (c *ForwardClient) Dispatch(ctx context.Context, incoming ForwardRequest, record func([]byte) error) (Response, error) {
	if c == nil {
		return Response{}, &TransportError{Kind: TransportDefinitive, Err: errors.New("forward client is required")}
	}
	endpoint := c.destination
	endpoint.RawQuery = incoming.RawQuery
	requestContext, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, incoming.Method, endpoint.String(), bytes.NewReader(incoming.Body))
	if err != nil {
		return Response{}, &TransportError{Kind: TransportDefinitive, Err: fmt.Errorf("build forward request: %w", err)}
	}
	request.Header = cloneForwardHeaders(incoming.Headers)
	removeHopByHopHeaders(request.Header)
	if _, present := request.Header["User-Agent"]; !present {
		// An explicit empty value prevents net/http from adding its own User-Agent.
		request.Header["User-Agent"] = []string{""}
	}
	if c.hostMode == ForwardHostOrigin {
		request.Host = incoming.Host
	}
	if record != nil {
		evidenceURL := *request.URL
		evidenceURL.RawQuery = ""
		evidence, err := json.Marshal(map[string]any{
			"version": 1, "method": request.Method, "url": evidenceURL.String(),
			"headers": map[string]any{"redacted": true}, "body_bytes": len(incoming.Body),
		})
		if err != nil {
			return Response{}, &TransportError{Kind: TransportDefinitive, Err: fmt.Errorf("encode forward request evidence: %w", err)}
		}
		if err := record(evidence); err != nil {
			return Response{}, &TransportError{Kind: TransportDefinitive, Err: fmt.Errorf("record forward request evidence: %w", err)}
		}
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return Response{}, &TransportError{Kind: classifyTransportError(err), Err: err}
	}
	defer response.Body.Close()
	responseBody, truncated, readErr := readBounded(response.Body, maxResponseBody)
	if readErr != nil {
		truncated = true
	}
	result := Response{StatusCode: response.StatusCode, Body: responseBody, Truncated: truncated, Accepted: response.StatusCode >= 200 && response.StatusCode < 300}
	if result.Accepted {
		return result, nil
	}
	return result, &TransportError{Kind: TransportDefinitive, Err: fmt.Errorf("unexpected HTTP status %d", response.StatusCode)}
}

func canonicalForwardURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("invalid forward URL")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	return parsed, nil
}

// CanonicalForwardURL returns the stable configured destination identity.
func CanonicalForwardURL(raw string) (string, error) {
	parsed, err := canonicalForwardURL(raw)
	if err != nil {
		return "", err
	}
	return parsed.String(), nil
}

func unsafeForwardHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address, err := netip.ParseAddr(host)
	return err == nil && (address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsPrivate() || address.IsUnspecified() || address.IsMulticast())
}

func cloneForwardHeaders(source http.Header) http.Header {
	cloned := make(http.Header, len(source))
	for name, values := range source {
		cloned[name] = append([]string(nil), values...)
	}
	return cloned
}

func removeHopByHopHeaders(headers http.Header) {
	for _, name := range headers.Values("Connection") {
		for _, token := range strings.Split(name, ",") {
			headers.Del(strings.TrimSpace(token))
		}
	}
	for _, name := range []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"} {
		headers.Del(name)
	}
}

func forwardTransport(source http.RoundTripper) (*http.Transport, bool) {
	if source == nil {
		return http.DefaultTransport.(*http.Transport).Clone(), true
	}
	transport, ok := source.(*http.Transport)
	if !ok {
		return nil, false
	}
	return transport.Clone(), true
}
