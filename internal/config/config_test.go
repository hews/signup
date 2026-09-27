package config

import "testing"

func TestBaseURL(t *testing.T) {
	t.Setenv("SIGNUP_BASE_URL", "https://sheets.example.org/")
	cfg, err := FromEnv()
	if err != nil || cfg.BaseURL != "https://sheets.example.org" {
		t.Fatalf("BaseURL = %q, %v; want the URL without its trailing slash", cfg.BaseURL, err)
	}
	t.Setenv("SIGNUP_BASE_URL", "sheets.example.org")
	if _, err := FromEnv(); err == nil {
		t.Fatal("a base URL without a scheme was accepted")
	}
	t.Setenv("SIGNUP_BASE_URL", "")
	if cfg, err := FromEnv(); err != nil || cfg.BaseURL != "" {
		t.Fatalf("unset: BaseURL = %q, %v", cfg.BaseURL, err)
	}
}
