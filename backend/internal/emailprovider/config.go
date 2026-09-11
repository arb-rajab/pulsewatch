package emailprovider

import (
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"strconv"
)

// defaultPort is 587 (SMTP submission, RFC 6409) — the port every major
// transactional-email provider (and this repo's own docker-compose default,
// see .env.example) expects STARTTLS on. 465 (implicit TLS) is supported via
// Config.ImplicitTLS for relays that require it instead.
const defaultPort = 587

// Config is one operator's outgoing mail relay account — a single,
// repo-wide piece of infrastructure configuration, not a per-channel
// secret. This is the one deliberate difference from ADR-0007's push
// credential, which is per-channel because FCM/APNs are fixed provider
// endpoints an operator brings their own credential to; SMTP has no such
// fixed endpoint; the relay itself is part of the configuration. A "email"
// alert_channels row's destination_encrypted still holds exactly what
// migration 000008 always said it would: the recipient address, decrypted
// per-dispatch the same way a webhook URL is (alerting.Channel).
type Config struct {
	Host     string
	Port     int
	Username string
	Password string
	// From is the envelope-from and Header From: address. Required: an SMTP
	// relay cannot accept a MAIL FROM with no address, and an email with no
	// legible sender is exactly the kind of silent-looking failure ADR-0006
	// warned against for this channel type.
	From string
	// ImplicitTLS selects TLS-from-the-first-byte (port 465 style) instead
	// of a plaintext connection with an opportunistic STARTTLS upgrade
	// (port 587 style, the default). Both are real, current SMTP submission
	// conventions; which one a given relay wants is a property of that
	// relay, not something this client can detect safely in advance.
	ImplicitTLS bool
	// RootCAs overrides the system trust store used to verify the relay's
	// TLS certificate. nil (the production default) means "trust the
	// system's own CA bundle" — the same default crypto/tls.Config already
	// has. This exists for the same real reason a self-hosted operator
	// might point ALERT_CHANNEL_ENCRYPTION_KEY-adjacent infrastructure at
	// an internal relay behind a private CA, and it is what lets this
	// package's own tests exercise a real STARTTLS/implicit-TLS handshake
	// against a real self-signed test certificate rather than skipping
	// certificate verification outright (which production never does).
	RootCAs *x509.CertPool
}

// Validate reports whether cfg has enough to attempt a send. It does not
// dial anything — a relay that is unreachable or refuses these credentials
// is Client.Send's job to discover and classify.
func (c Config) Validate() error {
	var missing []string
	if c.Host == "" {
		missing = append(missing, "SMTP_HOST")
	}
	if c.From == "" {
		missing = append(missing, "SMTP_FROM_ADDRESS")
	}
	if len(missing) > 0 {
		return fmt.Errorf("smtp relay is not configured: missing %v", missing)
	}
	if (c.Username == "") != (c.Password == "") {
		return errors.New("smtp relay config must set both SMTP_USERNAME and SMTP_PASSWORD, or neither")
	}
	return nil
}

// ConfigFromEnv loads the relay account from SMTP_HOST/SMTP_PORT (default
// 587)/SMTP_USERNAME/SMTP_PASSWORD/SMTP_FROM_ADDRESS/SMTP_IMPLICIT_TLS —
// the same "unset degrades gracefully, a present-but-malformed value fails
// loudly" discipline alerting.EncryptionKeyFromEnv already established for
// ALERT_CHANNEL_ENCRYPTION_KEY. An unset SMTP_HOST is the current
// production reality for a fresh install (no operator has configured an
// email channel yet) and is reported back as a plain error for the caller
// to log and degrade from, exactly as EncryptionKeyFromEnv does.
func ConfigFromEnv() (Config, error) {
	cfg := Config{
		Host:     os.Getenv("SMTP_HOST"),
		Username: os.Getenv("SMTP_USERNAME"),
		Password: os.Getenv("SMTP_PASSWORD"),
		From:     os.Getenv("SMTP_FROM_ADDRESS"),
		Port:     defaultPort,
	}
	if v := os.Getenv("SMTP_PORT"); v != "" {
		port, err := strconv.Atoi(v)
		if err != nil || port <= 0 || port > 65535 {
			return Config{}, fmt.Errorf("SMTP_PORT must be a valid port number, got %q", v)
		}
		cfg.Port = port
	}
	if v := os.Getenv("SMTP_IMPLICIT_TLS"); v != "" {
		implicit, err := strconv.ParseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("SMTP_IMPLICIT_TLS must be a boolean, got %q", v)
		}
		cfg.ImplicitTLS = implicit
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}
