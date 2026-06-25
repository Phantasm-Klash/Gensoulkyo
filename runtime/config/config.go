package config

import "time"

const (
	CurrentRulesetVersion = "open-alpha-0"
	DefaultTickRate       = 30
	DefaultSnapshotEvery  = 3
	DefaultDeckSize       = 20
	DefaultDeckCopyLimit  = 2
)

type ServerConfig struct {
	RulesetVersion string    `json:"ruleset_version"`
	ConfigVersion  string    `json:"config_version"`
	TickRate       int       `json:"tick_rate"`
	SnapshotEvery  int       `json:"snapshot_every"`
	DeckSize       int       `json:"deck_size"`
	DeckCopyLimit  int       `json:"deck_copy_limit"`
	GeneratedAt    time.Time `json:"generated_at"`
}

func Current() ServerConfig {
	return ServerConfig{
		RulesetVersion: CurrentRulesetVersion,
		ConfigVersion:  CurrentRulesetVersion,
		TickRate:       DefaultTickRate,
		SnapshotEvery:  DefaultSnapshotEvery,
		DeckSize:       DefaultDeckSize,
		DeckCopyLimit:  DefaultDeckCopyLimit,
		GeneratedAt:    time.Now().UTC(),
	}
}
