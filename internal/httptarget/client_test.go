package httptarget

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sync/atomic"
	"testing"
)

func TestClientDispatchRendersAuthenticatedIdempotentRequest(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/deploy" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("X-API-Token"); got != "http-secret" {
			t.Fatalf("API token = %q", got)
		}
		if got := r.Header.Get("Idempotency-Key"); got != "delivery-123" {
			t.Fatalf("Idempotency-Key = %q", got)
		}
		if got := r.Header.Get("X-Deploy-Source"); got != "hookfly" {
			t.Fatalf("X-Deploy-Source = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"result":"\u0068\u0074\u0074\u0070-secret","state":"accepted"}`))
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "http-secret", "X-API-Token", nil)
	if err != nil {
		t.Fatal(err)
	}
	var evidence []byte
	response, err := client.Dispatch(context.Background(), Request{
		Method: http.MethodPost, Path: "/api/deploy", Headers: map[string]string{"X-Deploy-Source": "hookfly"},
		Body: &Body{Type: "json", Value: map[string]any{"ref": "{{event.ref}}", "attempt_id": "{{attempt.id}}"}}, SuccessStatuses: []int{http.StatusAccepted},
	}, Values{EventRef: "main", AttemptID: "attempt-123", DeliveryID: "delivery-123"}, func(value []byte) error {
		evidence = append([]byte(nil), value...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !response.Accepted || response.StatusCode != http.StatusAccepted {
		t.Fatalf("response = %#v", response)
	}
	if !reflect.DeepEqual(gotBody, map[string]any{"ref": "main", "attempt_id": "attempt-123"}) {
		t.Fatalf("body = %#v", gotBody)
	}
	var recorded map[string]any
	if err := json.Unmarshal(evidence, &recorded); err != nil {
		t.Fatal(err)
	}
	if len(evidence) == 0 || bytes.Contains(evidence, []byte("http-secret")) {
		t.Fatalf("request evidence leaked token: %s", evidence)
	}
	if bytes.Contains(response.Body, []byte("http-secret")) {
		t.Fatalf("response evidence leaked token: %s", response.Body)
	}
	var responseEvidence map[string]any
	if err := json.Unmarshal(response.Body, &responseEvidence); err != nil {
		t.Fatal(err)
	}
	if got := responseEvidence["result"]; got != "[REDACTED]" {
		t.Fatalf("response result = %#v", got)
	}
}

func TestClientRejectsRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://other.example.invalid/deploy", http.StatusFound)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "token", "X-API-Token", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Dispatch(context.Background(), Request{Method: http.MethodPost, Path: "/deploy"}, Values{AttemptID: "attempt"}, nil)
	if err == nil || response.StatusCode != http.StatusFound {
		t.Fatalf("Dispatch() = %#v/%v, want redirect failure", response, err)
	}
}

func TestClientDispatchAllowsAbsentBodyAndHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "" {
			t.Fatalf("Content-Type = %q", r.Header.Get("Content-Type"))
		}
		if r.Header.Get("X-Optional") != "" {
			t.Fatalf("X-Optional = %q", r.Header.Get("X-Optional"))
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || len(body) != 0 {
			t.Fatalf("body/error = %q/%v", body, err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "token", "X-API-Token", nil)
	if err != nil {
		t.Fatal(err)
	}
	var evidence []byte
	_, err = client.Dispatch(context.Background(), Request{Method: http.MethodPost, Path: "/deploy"}, Values{AttemptID: "attempt"}, func(value []byte) error {
		evidence = append([]byte(nil), value...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var recorded map[string]any
	if err := json.Unmarshal(evidence, &recorded); err != nil {
		t.Fatal(err)
	}
	if _, found := recorded["body"]; found {
		t.Fatalf("request evidence has unexpected body: %s", evidence)
	}
}

func TestClientDispatchRendersQueryFormAndRawBodies(t *testing.T) {
	for _, test := range []struct {
		name            string
		request         Request
		wantContentType string
		wantBody        string
		wantQuery       url.Values
	}{
		{
			name:            "form",
			request:         Request{Method: http.MethodPost, Path: "/deploy", Query: map[string][]string{"page": {"2", "3"}, "source": {"{{ event.source }}"}}, Body: &Body{Type: "form", Value: map[string][]string{"name": {"foo"}, "revision": {"{{ event.revision }}"}}}},
			wantContentType: "application/x-www-form-urlencoded",
			wantBody:        "name=foo&revision=abc123",
			wantQuery:       url.Values{"page": {"2", "3"}, "source": {"gitlab"}},
		},
		{
			name:            "raw",
			request:         Request{Method: http.MethodPost, Path: "/deploy", Body: &Body{Type: "raw", ContentType: "text/plain", Value: "deploy {{ event.ref }}"}},
			wantContentType: "text/plain",
			wantBody:        "deploy main",
			wantQuery:       url.Values{},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.Header.Get("Content-Type"); got != test.wantContentType {
					t.Fatalf("Content-Type = %q", got)
				}
				if got := r.URL.Query(); !reflect.DeepEqual(got, test.wantQuery) {
					t.Fatalf("query = %#v", got)
				}
				body, err := io.ReadAll(r.Body)
				if err != nil || string(body) != test.wantBody {
					t.Fatalf("body/error = %q/%v", body, err)
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			client, err := NewClient(server.URL, "token", "X-API-Token", nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Dispatch(context.Background(), test.request, Values{EventSource: "gitlab", EventRef: "main", EventRevision: "abc123", DeliveryID: "delivery"}, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAuthenticatedClientUsesBearerAuthentication(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer bearer-token" {
			t.Fatalf("Authorization = %q", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client, err := NewAuthenticatedClient(server.URL, Authentication{Type: "bearer", Value: "bearer-token"}, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Dispatch(context.Background(), Request{Method: http.MethodPost, Path: "/deploy"}, Values{DeliveryID: "delivery"}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestAuthenticatedClientRejectsResolvedPrivateNetworkWithoutOptIn(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client, err := NewAuthenticatedClient(server.URL, Authentication{Type: "bearer", Value: "token"}, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Dispatch(context.Background(), Request{Method: http.MethodPost, Path: "/deploy"}, Values{DeliveryID: "delivery"}, nil); err == nil {
		t.Fatal("Dispatch() error = nil, want private network rejection")
	}
	if calls.Load() != 0 {
		t.Fatalf("private target received %d requests", calls.Load())
	}
}
