package internal

import (
	"errors"
	"fmt"
	"strings"
)

// ErrAuthMissing is returned when required Cloudflare credentials are absent.
var ErrAuthMissing = errors.New("cloudflare: api_token not configured")

// Config is the provider config block consumed from config_json.
type Config struct {
	APIToken string
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.APIToken) == "" {
		return fmt.Errorf("%w: api_token is empty", ErrAuthMissing)
	}
	return nil
}
