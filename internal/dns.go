package internal

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// ErrAuthMissing is returned when required Cloudflare credentials are absent.
var ErrAuthMissing = errors.New("cloudflare: api_token not configured")

// Config is the provider config block consumed from config_json.
type Config struct {
	APIToken       string
	AccountID      string
	RequestTimeout time.Duration
}

func ConfigFromMap(m map[string]any) (Config, error) {
	cfg := Config{
		APIToken:  strVal(m, "api_token"),
		AccountID: strVal(m, "account_id"),
	}
	if cfg.APIToken == "" {
		cfg.APIToken = strVal(m, "token")
	}
	rawTimeout := strVal(m, "request_timeout")
	if rawTimeout == "" {
		rawTimeout = strings.TrimSpace(os.Getenv("CLOUDFLARE_REQUEST_TIMEOUT"))
	}
	if rawTimeout != "" {
		timeout, err := time.ParseDuration(rawTimeout)
		if err != nil {
			return Config{}, fmt.Errorf("request_timeout %q is not a valid Go duration: %w", rawTimeout, err)
		}
		if timeout <= 0 {
			return Config{}, fmt.Errorf("request_timeout must be greater than zero")
		}
		cfg.RequestTimeout = timeout
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.APIToken) == "" {
		return fmt.Errorf("%w: api_token is empty", ErrAuthMissing)
	}
	return nil
}
