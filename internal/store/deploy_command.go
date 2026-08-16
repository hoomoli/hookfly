package store

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/hoomoli/hookfly/internal/dokploy"
)

type deploymentRequestEvidence struct {
	Version int               `json:"version"`
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    deploymentRequest `json:"body"`
}

type deploymentRequest struct {
	ComposeID   string `json:"composeId"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
}

func deploymentCurl(raw, targetSnapshot []byte, recorded bool) *string {
	if !recorded {
		return nil
	}
	var target struct {
		BindingVersion int    `json:"binding_version"`
		Type           string `json:"type"`
		ResourceType   string `json:"resource_type"`
		ResourceID     string `json:"resource_id"`
		Connection     struct {
			BaseURL string `json:"base_url"`
		} `json:"connection"`
	}
	if err := json.Unmarshal(targetSnapshot, &target); err != nil || target.BindingVersion != 2 ||
		target.Type != "dokploy" || target.ResourceType != "compose" || target.ResourceID == "" {
		return nil
	}
	baseURL, err := dokploy.CanonicalBaseURL(target.Connection.BaseURL)
	if err != nil {
		return nil
	}
	var evidence deploymentRequestEvidence
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&evidence); err != nil {
		return nil
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil
	}
	if evidence.Version != 1 || evidence.Method != http.MethodPost || evidence.Body.ComposeID == "" ||
		len(evidence.Headers) != 1 || evidence.Headers["Content-Type"] != "application/json" {
		return nil
	}
	endpoint, err := url.Parse(evidence.URL)
	if err != nil || endpoint.User != nil || endpoint.Host == "" ||
		(endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Path != "/api/compose.deploy" ||
		endpoint.RawPath != "" || endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" ||
		evidence.URL != baseURL+"/api/compose.deploy" || evidence.Body.ComposeID != target.ResourceID {
		return nil
	}
	body, err := json.Marshal(evidence.Body)
	if err != nil {
		return nil
	}
	command := "curl --request POST \\\n" +
		"  --url " + shellSingleQuote(evidence.URL) + " \\\n" +
		"  --header 'Content-Type: application/json' \\\n" +
		"  --header \"x-api-key: ${DOKPLOY_API_KEY}\" \\\n" +
		"  --data " + shellSingleQuote(string(body))
	return &command
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
