package dokploy

import (
	"encoding/json"
	"net/http"
)

const deployRequestEvidenceVersion = 1

type deployRequestEvidence struct {
	Version int               `json:"version"`
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    DeployRequest     `json:"body"`
}

func (c *Client) deployRequestEvidence(request DeployRequest) ([]byte, error) {
	return json.Marshal(deployRequestEvidence{
		Version: deployRequestEvidenceVersion,
		Method:  http.MethodPost,
		URL:     c.deployEndpoint,
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    request,
	})
}
