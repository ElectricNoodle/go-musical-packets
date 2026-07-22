package config

import (
	"reflect"
	"testing"
)

func TestConfigRedactedAndResolvedRoundTripSecretsWithoutAliases(t *testing.T) {
	current := Default()
	current.Mapping.Seed = "private-flow-seed"
	current.Peer.Enabled = true
	current.Peer.URL = "wss://example.test/notes?token=secret"
	current.Peer.Token = "peer-bearer-secret"
	current.Rules = RulesConfig{{
		ID:     "copy",
		Match:  RuleMatchConfig{SourcePorts: &PortRangeConfig{Minimum: 1, Maximum: 2}},
		Action: RuleActionConfig{State: FlowMonitor},
	}}

	redacted := current.Redacted()
	if redacted.Mapping.Seed != RedactedValue || redacted.Peer.URL != RedactedURLValue || redacted.Peer.Token != RedactedValue {
		t.Fatalf("Redacted() secrets = (%q, %q, %q)", redacted.Mapping.Seed, redacted.Peer.URL, redacted.Peer.Token)
	}
	if _, err := Encode(redacted); err != nil {
		t.Fatalf("Encode(Redacted()) error = %v", err)
	}
	redacted.Rules[0].Match.SourcePorts.Minimum = 99
	if current.Rules[0].Match.SourcePorts.Minimum != 1 {
		t.Fatal("Redacted() leaked a mutable rule alias")
	}

	redacted = current.Redacted()
	resolved, err := ResolveRedacted(redacted, current)
	if err != nil {
		t.Fatalf("ResolveRedacted() error = %v", err)
	}
	if !reflect.DeepEqual(resolved, current) {
		t.Fatalf("ResolveRedacted() = %#v, want original", resolved)
	}
	resolved.Mapping.Seed = "changed"
	if current.Mapping.Seed != "private-flow-seed" {
		t.Fatal("ResolveRedacted() leaked a mutable configuration alias")
	}
}

func TestResolveRedactedRejectsPlaceholderWithoutActiveSecret(t *testing.T) {
	current := Default()
	current.Peer.URL = ""
	current.Peer.Token = ""
	candidate := current.Clone()
	candidate.Peer.URL = RedactedURLValue
	if _, err := ResolveRedacted(candidate, current); err == nil {
		t.Fatal("ResolveRedacted() error = nil")
	}
}

func TestResolveRedactedRejectsConcreteActiveSecretGuessesIdentically(t *testing.T) {
	current := Default()
	current.Mapping.Seed = "actual-secret"
	current.Peer.URL = "wss://actual.example.test/?token=secret"
	current.Peer.Token = "actual-peer-token"

	var seedError string
	for _, guess := range []string{"wrong-secret", current.Mapping.Seed} {
		candidate := current.Redacted()
		candidate.Mapping.Seed = guess
		_, err := ResolveRedacted(candidate, current)
		if err == nil {
			t.Fatalf("ResolveRedacted(seed %q) error = nil", guess)
		}
		if seedError == "" {
			seedError = err.Error()
		} else if err.Error() != seedError {
			t.Fatalf("seed guess errors differ: %q and %q", seedError, err)
		}
	}

	var peerError string
	for _, guess := range []string{"wss://wrong.example.test/", current.Peer.URL} {
		candidate := current.Redacted()
		candidate.Peer.URL = guess
		_, err := ResolveRedacted(candidate, current)
		if err == nil {
			t.Fatalf("ResolveRedacted(peer %q) error = nil", guess)
		}
		if peerError == "" {
			peerError = err.Error()
		} else if err.Error() != peerError {
			t.Fatalf("peer guess errors differ: %q and %q", peerError, err)
		}
	}

	var tokenError string
	for _, guess := range []string{"wrong-peer-token", current.Peer.Token} {
		candidate := current.Redacted()
		candidate.Peer.Token = guess
		_, err := ResolveRedacted(candidate, current)
		if err == nil {
			t.Fatalf("ResolveRedacted(peer token %q) error = nil", guess)
		}
		if tokenError == "" {
			tokenError = err.Error()
		} else if err.Error() != tokenError {
			t.Fatalf("peer token guess errors differ: %q and %q", tokenError, err)
		}
	}
}
