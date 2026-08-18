package httptarget

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestForwardDispatchPreservesOriginalRequestAndUsesTargetHostByDefault(t *testing.T) {
	// Break caught: rebuilding a webhook from normalized fields or dropping sensitive/repeated headers.
	body := []byte("{\"ref\":\"main\",\"space\":\"a b\"}")
	var gotMethod, gotQuery, gotHost string
	var gotHeader http.Header
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotQuery, gotHost = r.Method, r.URL.RawQuery, r.Host
		gotHeader = r.Header.Clone()
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client, err := NewForwardClient(server.URL+"/receiver", "", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	headers := http.Header{
		"Content-Type":        {"application/json"},
		"X-Gitlab-Token":      {"gitlab-secret"},
		"X-Hub-Signature-256": {"sha256=github-signature"},
		"X-Repeated":          {"first", "second"},
		"Origin":              {"https://gitlab.example.invalid"},
		"Connection":          {"X-Hop"},
		"X-Hop":               {"must-not-forward"},
		"User-Agent":          {"GitLab/17.0"},
	}
	response, err := client.Dispatch(context.Background(), ForwardRequest{
		Method: "POST", RawQuery: "token=a%2Bb&token=second", Host: "hookfly.example.invalid",
		Headers: headers, Body: body,
	}, nil)
	if err != nil || !response.Accepted {
		t.Fatalf("Dispatch() = %#v, %v", response, err)
	}
	if gotMethod != "POST" || gotQuery != "token=a%2Bb&token=second" || gotHost != server.Listener.Addr().String() {
		t.Fatalf("request line = %s ?%s host=%s", gotMethod, gotQuery, gotHost)
	}
	if !reflect.DeepEqual(gotBody, body) {
		t.Fatalf("body = %q, want %q", gotBody, body)
	}
	for name, want := range map[string][]string{
		"Content-Type":        {"application/json"},
		"X-Gitlab-Token":      {"gitlab-secret"},
		"X-Hub-Signature-256": {"sha256=github-signature"},
		"X-Repeated":          {"first", "second"},
		"Origin":              {"https://gitlab.example.invalid"},
		"User-Agent":          {"GitLab/17.0"},
	} {
		if got := gotHeader.Values(name); !reflect.DeepEqual(got, want) {
			t.Fatalf("header %s = %#v, want %#v", name, got, want)
		}
	}
	if gotHeader.Get("Connection") != "" || gotHeader.Get("X-Hop") != "" {
		t.Fatalf("hop-by-hop headers forwarded: %#v", gotHeader)
	}
}

func TestForwardDispatchCanPreserveOriginHost(t *testing.T) {
	// Break caught: treating origin mode as the default target-host mode.
	var gotHost string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	client, err := NewForwardClient(server.URL+"/receiver", "origin", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Dispatch(context.Background(), ForwardRequest{Method: "POST", Host: "hookfly.example.invalid", Body: []byte("payload")}, nil); err != nil {
		t.Fatal(err)
	}
	if gotHost != "hookfly.example.invalid" {
		t.Fatalf("Host = %q", gotHost)
	}
}

func TestNewForwardClientRejectsUnsafeDestinationsAndHostModes(t *testing.T) {
	// Break caught: allowing credentials, fragments, or private addresses without explicit opt-in.
	for _, test := range []struct {
		name, destination, hostMode string
		allowPrivate                bool
	}{
		{name: "credentials", destination: "https://user@example.invalid/hook"},
		{name: "fragment", destination: "https://example.invalid/hook#fragment"},
		{name: "private", destination: "http://127.0.0.1/hook"},
		{name: "host mode", destination: "https://example.invalid/hook", hostMode: "incoming"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewForwardClient(test.destination, test.hostMode, test.allowPrivate, nil); err == nil {
				t.Fatal("NewForwardClient() accepted unsafe configuration")
			}
		})
	}
}
