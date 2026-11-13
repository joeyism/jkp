package irc

import "testing"

func TestNowUnixUsesNanosecondPrecisionForReplayCursor(t *testing.T) {
	if got := nowUnix(); got < 1_000_000_000_000_000 {
		t.Fatalf("nowUnix() = %d, want a nanosecond-scale timestamp", got)
	}
}
