package config

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestRenderCredentialValue(t *testing.T) {
	const token = "PLACEHOLDER-TOKEN"

	basicWant := "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token))

	cases := []struct {
		name     string
		template string
		username string
		want     string
	}{
		{"bearer", "Bearer {token}", "", "Bearer " + token},
		{"token-scheme", "token {token}", "", "token " + token},
		{"bare", "{token}", "", token},
		{"basic-base64", "Basic base64({user}:{token})", "x-access-token", basicWant},
		{"custom-value", "{token}", "", token}, // header name is a separate field, not the template
		{"prefix-and-suffix", "SSWS {token}", "", "SSWS " + token},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RenderCredentialValue(tc.template, tc.username, token)
			if err != nil {
				t.Fatalf("RenderCredentialValue(%q) error: %v", tc.template, err)
			}
			if got != tc.want {
				t.Errorf("RenderCredentialValue(%q) = %q, want %q", tc.template, got, tc.want)
			}
			// A rendered value must never still carry a placeholder.
			if strings.Contains(got, "{token}") || strings.Contains(got, "{user}") {
				t.Errorf("rendered value %q still contains a placeholder", got)
			}
		})
	}
}

func TestValidateTemplate_Rejects(t *testing.T) {
	cases := []struct {
		name     string
		template string
		username string
	}{
		{"no token placeholder", "Bearer static", ""},
		{"user without username", "Basic base64({user}:{token})", ""},
		{"unbalanced base64", "Basic base64({user}:{token}", "x"},
		{"double base64", "base64({token}) base64({token})", ""},
		{"unknown placeholder", "Bearer {token} {scope}", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateTemplate(tc.template, tc.username); err == nil {
				t.Fatalf("validateTemplate(%q, %q) = nil, want error", tc.template, tc.username)
			}
			// RenderCredentialValue validates first, so it must reject too.
			if _, err := RenderCredentialValue(tc.template, tc.username, "T"); err == nil {
				t.Errorf("RenderCredentialValue(%q) = nil error, want error", tc.template)
			}
		})
	}
}

func TestValidateTemplate_Accepts(t *testing.T) {
	cases := []struct {
		template string
		username string
	}{
		{"Bearer {token}", ""},
		{"token {token}", ""},
		{"{token}", ""},
		{"Basic base64({user}:{token})", "__token__"},
		{"PRIVATE-TOKEN {token}", ""},
	}
	for _, tc := range cases {
		if err := validateTemplate(tc.template, tc.username); err != nil {
			t.Errorf("validateTemplate(%q, %q) = %v, want nil", tc.template, tc.username, err)
		}
	}
}
