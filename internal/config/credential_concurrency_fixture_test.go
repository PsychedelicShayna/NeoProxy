package config

import (
	"fmt"
	"testing"
	"time"
)

type credentialConcurrencyFixtureWireConfig struct {
	LifecycleConfigRevision    int64
	ObservationBarrierRevision int64
	CPAHeartbeatTimeout        time.Duration
	CPACancelBound             time.Duration
	ReclaimGrace               time.Duration
	CleanupInterval            time.Duration
	ReleaseFlushInterval       string
	ReleaseMaxBackoff          string
	BusyRetryMin               string
	BusyRetryMax               string
	MaxLimit                   int64
}

func (c credentialConcurrencyFixtureWireConfig) config() (CredentialConcurrencyConfig, error) {
	var hot struct {
		ReleaseFlushInterval time.Duration
		ReleaseMaxBackoff    time.Duration
		BusyRetryMin         time.Duration
		BusyRetryMax         time.Duration
	}
	for _, field := range []struct {
		name  string
		raw   string
		value *time.Duration
	}{
		{"release-flush-interval", c.ReleaseFlushInterval, &hot.ReleaseFlushInterval},
		{"release-max-backoff", c.ReleaseMaxBackoff, &hot.ReleaseMaxBackoff},
		{"busy-retry-min", c.BusyRetryMin, &hot.BusyRetryMin},
		{"busy-retry-max", c.BusyRetryMax, &hot.BusyRetryMax},
	} {
		duration, err := time.ParseDuration(field.raw)
		if err != nil {
			return CredentialConcurrencyConfig{}, fmt.Errorf("parse fixture %s: %w", field.name, err)
		}
		*field.value = duration
	}
	return CredentialConcurrencyConfig{
		LifecycleConfigRevision:    c.LifecycleConfigRevision,
		ObservationBarrierRevision: c.ObservationBarrierRevision,
		CPAHeartbeatTimeout:        c.CPAHeartbeatTimeout,
		CPACancelBound:             c.CPACancelBound,
		ReclaimGrace:               c.ReclaimGrace,
		CleanupInterval:            c.CleanupInterval,
		ReleaseFlushInterval:       hot.ReleaseFlushInterval,
		ReleaseMaxBackoff:          hot.ReleaseMaxBackoff,
		BusyRetryMin:               hot.BusyRetryMin,
		BusyRetryMax:               hot.BusyRetryMax,
		MaxLimit:                   c.MaxLimit,
	}, nil
}

func credentialConcurrencyWireFixture(cpaHeartbeatTimeout time.Duration) credentialConcurrencyFixtureWireConfig {
	return credentialConcurrencyFixtureWireConfig{
		CPAHeartbeatTimeout:  cpaHeartbeatTimeout,
		CPACancelBound:       5 * time.Second,
		ReclaimGrace:         5 * time.Second,
		CleanupInterval:      5 * time.Second,
		ReleaseFlushInterval: "250ms",
		ReleaseMaxBackoff:    "2s",
		BusyRetryMin:         "250ms",
		BusyRetryMax:         "1s",
		MaxLimit:             1_000_000,
	}
}

func credentialConcurrencyConfigFixture(cpaHeartbeatTimeout time.Duration) CredentialConcurrencyConfig {
	return CredentialConcurrencyConfig{
		CPAHeartbeatTimeout:  cpaHeartbeatTimeout,
		CPACancelBound:       5 * time.Second,
		ReclaimGrace:         5 * time.Second,
		CleanupInterval:      5 * time.Second,
		ReleaseFlushInterval: 250 * time.Millisecond,
		ReleaseMaxBackoff:    2 * time.Second,
		BusyRetryMin:         250 * time.Millisecond,
		BusyRetryMax:         time.Second,
		MaxLimit:             1_000_000,
	}
}

func TestCredentialConcurrencyLifecycleFixture(t *testing.T) {
	wireDefaults := credentialConcurrencyWireFixture(3 * time.Second)
	wireDefaults.LifecycleConfigRevision = 1
	defaults, errConfig := wireDefaults.config()
	if errConfig != nil {
		t.Fatal(errConfig)
	}

	expectedDefaults := credentialConcurrencyConfigFixture(3 * time.Second)
	expectedDefaults.LifecycleConfigRevision = 1
	if defaults != expectedDefaults {
		t.Fatalf("defaults = %#v, want %#v", defaults, expectedDefaults)
	}
	if errValidate := ValidateCredentialConcurrency(defaults); errValidate != nil {
		t.Fatalf("ValidateCredentialConcurrency(defaults) error = %v", errValidate)
	}

	invalidFixtures := []struct {
		NodeHeartbeatTimeout time.Duration
		Config               credentialConcurrencyFixtureWireConfig
	}{
		{NodeHeartbeatTimeout: 3 * time.Second, Config: credentialConcurrencyWireFixture(3 * time.Second)},
		{NodeHeartbeatTimeout: 20 * time.Second, Config: credentialConcurrencyWireFixture(0)},
	}
	expectedInvalid := []struct {
		nodeHeartbeatTimeout time.Duration
		config               CredentialConcurrencyConfig
	}{
		{nodeHeartbeatTimeout: 3 * time.Second, config: credentialConcurrencyConfigFixture(3 * time.Second)},
		{nodeHeartbeatTimeout: 20 * time.Second, config: credentialConcurrencyConfigFixture(0)},
	}
	if len(invalidFixtures) != len(expectedInvalid) {
		t.Fatalf("invalid fixture count = %d, want %d", len(invalidFixtures), len(expectedInvalid))
	}
	for index, expected := range expectedInvalid {
		item := invalidFixtures[index]
		itemConfig, errConfig := item.Config.config()
		if errConfig != nil {
			t.Fatalf("invalid fixture %d config() error = %v", index, errConfig)
		}
		if item.NodeHeartbeatTimeout != expected.nodeHeartbeatTimeout || itemConfig != expected.config {
			t.Fatalf("invalid fixture %d = %#v, want node heartbeat timeout %s and config %#v", index, itemConfig, expected.nodeHeartbeatTimeout, expected.config)
		}
		if errValidate := ValidateCredentialConcurrencyLifecycle(item.NodeHeartbeatTimeout, itemConfig); errValidate == nil {
			t.Fatalf("invalid fixture %d passed", index)
		}
	}
}
