package rollup

import (
	"testing"
	"time"
)

// TestCadenceGapExceeded checks the WARN threshold Run uses to surface a
// real cadence violation (Session 13: the dev machine's Docker VM pausing
// during host idle produced real multi-hour gaps between ticks with no
// error logged anywhere) without false-positiving on ordinary scheduling
// jitter around a normal, on-time tick.
func TestCadenceGapExceeded(t *testing.T) {
	cfg := Config{TickInterval: time.Hour}

	cases := []struct {
		name string
		gap  time.Duration
		want bool
	}{
		{"on time", time.Hour, false},
		{"small jitter under load", time.Hour + 2*time.Minute, false},
		{"just under the 10% tolerance", time.Hour + 5*time.Minute, false},
		{"just over the 10% tolerance", time.Hour + 7*time.Minute, true},
		{"host/VM paused for hours", 13*time.Hour + 33*time.Minute, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cadenceGapExceeded(cfg, tc.gap); got != tc.want {
				t.Errorf("cadenceGapExceeded(%v) = %v, want %v", tc.gap, got, tc.want)
			}
		})
	}
}
