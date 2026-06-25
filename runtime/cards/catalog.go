package cards

import "github.com/Phantasm-Klash/Gensoulkyo/runtime/config"

var OpenAlphaCatalog = map[string]Definition{
	"pulse_shot":     {Code: "pulse_shot", Enabled: true},
	"focus_barrier":  {Code: "focus_barrier", Enabled: true},
	"graze_spark":    {Code: "graze_spark", Enabled: true},
	"drift_cancel":   {Code: "drift_cancel", Enabled: true},
	"spiral_field":   {Code: "spiral_field", Enabled: true},
	"mirror_step":    {Code: "mirror_step", Enabled: true},
	"bomb_fragment":  {Code: "bomb_fragment", Enabled: true},
	"score_lantern":  {Code: "score_lantern", Enabled: true},
	"slow_charm":     {Code: "slow_charm", Enabled: true},
	"tempo_seal":     {Code: "tempo_seal", Enabled: true},
	"needle_thread":  {Code: "needle_thread", Enabled: true},
	"orbit_wisp":     {Code: "orbit_wisp", Enabled: true},
	"safe_lane":      {Code: "safe_lane", Enabled: true},
	"chain_voltage":  {Code: "chain_voltage", Enabled: true},
	"burst_window":   {Code: "burst_window", Enabled: true},
	"anchor_sigil":   {Code: "anchor_sigil", Enabled: true},
	"starlit_feint":  {Code: "starlit_feint", Enabled: true},
	"reversal_glyph": {Code: "reversal_glyph", Enabled: true},
	"crossfire_mark": {Code: "crossfire_mark", Enabled: true},
	"last_guard":     {Code: "last_guard", Enabled: true},
}

func DefaultOwnedCards() map[string]int {
	owned := make(map[string]int, len(OpenAlphaCatalog))
	for code, def := range OpenAlphaCatalog {
		if def.Enabled {
			owned[code] = config.DefaultDeckCopyLimit
		}
	}
	return owned
}
