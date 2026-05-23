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
