package redact

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestJSONMasksNestedSensitiveKeysCaseInsensitively(t *testing.T) {
	// Break caught: leaking a sensitive value nested in an object or array under mixed-case key names.
	input := []byte(`{"Token":"one","nested":{"client_SECRET":"two","items":[{"AUTHORIZATION":"three"},{"Api_Key":"four"},{"dbPassword":"five"}]},"safe":"visible"}`)
	output, truncated := JSON(input, 4096)
	if truncated {
		t.Fatal("small JSON was marked truncated")
	}
	if !json.Valid(output) {
		t.Fatalf("output is not JSON: %q", output)
	}
	for _, secret := range []string{"one", "two", "three", "four", "five"} {
		if bytes.Contains(output, []byte(secret)) {
			t.Fatalf("secret %q remains in %s", secret, output)
		}
	}
	if !bytes.Contains(output, []byte(`"safe":"visible"`)) {
		t.Fatalf("safe field missing from %s", output)
	}
}

func TestJSONNormalizesSecretKeySeparatorsAndCamelCase(t *testing.T) {
	// Break caught: leaking credentials whose field names use camelCase or hyphens instead of underscores.
	input := []byte(`{"apiKey":"one","api-key":"two","privateKey":"three","private_key":"four","safeKey":"visible"}`)
	output, truncated := JSON(input, 4096)
	if truncated {
		t.Fatal("small JSON was marked truncated")
	}
	for _, secret := range []string{"one", "two", "three", "four"} {
		if bytes.Contains(output, []byte(secret)) {
			t.Fatalf("secret %q remains in %s", secret, output)
		}
	}
	if !bytes.Contains(output, []byte(`"safeKey":"visible"`)) {
		t.Fatalf("ordinary key missing from %s", output)
	}
}

func TestJSONPreservesValidNonSensitiveJSON(t *testing.T) {
	// Break caught: treating ordinary JSON as opaque text or changing its data while redacting.
	input := []byte(` { "name": "demo", "items": [1, true, null] } `)
	output, truncated := JSON(input, 4096)
	if truncated || !json.Valid(output) {
		t.Fatalf("output/truncated = %q/%v", output, truncated)
	}
	var got, want any
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(input, &want); err != nil {
		t.Fatal(err)
	}
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("output = %s, want data %s", gotJSON, wantJSON)
	}
}

func TestJSONBoundsNonJSONOutput(t *testing.T) {
	// Break caught: storing an unbounded HTML/plain-text upstream error body.
	input := []byte("upstream proxy failure with a very long explanation")
	output, truncated := JSON(input, 24)
	if !truncated || len(output) > 24 {
		t.Fatalf("output length/truncated = %d/%v", len(output), truncated)
	}
	if !strings.Contains(string(output), "trunc") {
		t.Fatalf("truncation is not marked: %q", output)
	}

	output, truncated = JSON([]byte("plain failure"), 24)
	if truncated || string(output) != "plain failure" {
		t.Fatalf("short non-JSON = %q/%v", output, truncated)
	}
}

func TestJSONBoundsLargeJSONWithoutLeakingSensitiveValue(t *testing.T) {
	// Break caught: leaking an early secret when input exceeds the snapshot limit, or returning invalid truncated JSON.
	input := []byte(`{"api_key":"never-store-this","payload":"` + strings.Repeat("x", 1024) + `"}`)
	output, truncated := JSON(input, 64)
	if !truncated || len(output) > 64 {
		t.Fatalf("output length/truncated = %d/%v", len(output), truncated)
	}
	if !json.Valid(output) {
		t.Fatalf("bounded valid input became invalid JSON: %q", output)
	}
	if bytes.Contains(output, []byte("never-store-this")) {
		t.Fatalf("secret remains in %q", output)
	}
}

func TestJSONHandlesZeroLimit(t *testing.T) {
	// Break caught: panicking or returning bytes when the caller explicitly allows no snapshot data.
	output, truncated := JSON([]byte(`{"token":"secret"}`), 0)
	if len(output) != 0 || !truncated {
		t.Fatalf("output/truncated = %q/%v", output, truncated)
	}
}
