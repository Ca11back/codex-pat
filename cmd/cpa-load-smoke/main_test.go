package main

import "testing"

func TestValidateEmptyStatusResponse(t *testing.T) {
	t.Parallel()

	for _, body := range []string{
		`{}`,
		`{"data":{}}`,
		`{"data":{"accounts":null}}`,
		`{"data":{"accounts":[{"name":"unexpected.json"}]}}`,
		`not-json`,
	} {
		body := body
		t.Run(body, func(t *testing.T) {
			t.Parallel()
			if err := validateEmptyStatusResponse([]byte(body)); err == nil {
				t.Fatalf("validateEmptyStatusResponse(%q) succeeded, want error", body)
			}
		})
	}
	if err := validateEmptyStatusResponse([]byte(`{"data":{"accounts":[]}}`)); err != nil {
		t.Fatalf("validateEmptyStatusResponse(empty accounts) = %v", err)
	}
}
