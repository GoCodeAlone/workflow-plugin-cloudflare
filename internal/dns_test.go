package internal

import (
	"errors"
	"testing"
)

func TestConfigValidateRequiresAPIToken(t *testing.T) {
	err := (Config{}).Validate()
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrAuthMissing) {
		t.Fatalf("err = %v, want ErrAuthMissing", err)
	}
}

func TestConfigValidateAcceptsAPIToken(t *testing.T) {
	if err := (Config{APIToken: "token"}).Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestConfigFromMapParsesRequestTimeout(t *testing.T) {
	cfg, err := ConfigFromMap(map[string]any{
		"api_token":       "token",
		"request_timeout": "7s",
	})
	if err != nil {
		t.Fatalf("ConfigFromMap: %v", err)
	}
	if cfg.RequestTimeout.String() != "7s" {
		t.Fatalf("RequestTimeout = %s, want 7s", cfg.RequestTimeout)
	}
}

func TestConfigFromMapUsesRequestTimeoutEnvFallback(t *testing.T) {
	t.Setenv("CLOUDFLARE_REQUEST_TIMEOUT", "9s")
	cfg, err := ConfigFromMap(map[string]any{"api_token": "token"})
	if err != nil {
		t.Fatalf("ConfigFromMap: %v", err)
	}
	if cfg.RequestTimeout.String() != "9s" {
		t.Fatalf("RequestTimeout = %s, want 9s", cfg.RequestTimeout)
	}
}

func TestConfigFromMapRejectsInvalidRequestTimeout(t *testing.T) {
	_, err := ConfigFromMap(map[string]any{
		"api_token":       "token",
		"request_timeout": "nope",
	})
	if err == nil {
		t.Fatal("ConfigFromMap returned nil error, want invalid timeout")
	}
}
