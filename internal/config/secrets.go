package config

import "errors"

// RedactedValue is the reserved write-only placeholder used by management
// representations. Sending it back preserves the active secret value.
const RedactedValue = "<redacted>"

// RedactedURLValue is a schema-valid reserved placeholder for write-only URL
// fields, allowing redacted configurations to retain canonical encoding.
const RedactedURLValue = "wss://redacted.invalid/"

// Redacted returns a detached configuration safe for management responses.
// Keep this allowlist centralized as new secret-bearing fields are added.
func (config Config) Redacted() Config {
	redacted := config.Clone()
	if redacted.Mapping.Seed != "" {
		redacted.Mapping.Seed = RedactedValue
	}
	if redacted.Peer.URL != "" {
		redacted.Peer.URL = RedactedURLValue
	}
	return redacted
}

// ResolveRedacted replaces reserved write-only placeholders in candidate with
// values from current. When a secret is active, candidates must use its
// placeholder: accepting concrete guesses would turn validation into a secret
// equality oracle. Callers must resolve before validation and persistence.
func ResolveRedacted(candidate, current Config) (Config, error) {
	resolved := candidate.Clone()
	if current.Mapping.Seed != "" && resolved.Mapping.Seed != RedactedValue {
		return Config{}, errors.New("mapping.seed must use its write-only placeholder")
	}
	if current.Peer.URL != "" && resolved.Peer.URL != RedactedURLValue {
		return Config{}, errors.New("peer.url must use its write-only placeholder")
	}
	if resolved.Mapping.Seed == RedactedValue {
		if current.Mapping.Seed == "" {
			return Config{}, errors.New("mapping.seed redaction placeholder has no active value")
		}
		resolved.Mapping.Seed = current.Mapping.Seed
	}
	if resolved.Peer.URL == RedactedURLValue {
		if current.Peer.URL == "" {
			return Config{}, errors.New("peer.url redaction placeholder has no active value")
		}
		resolved.Peer.URL = current.Peer.URL
	}
	return resolved, nil
}
