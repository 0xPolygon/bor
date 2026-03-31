package common

import (
	"testing"
	"time"
)

func TestPrettyTimeUTCFormat(t *testing.T) {
	input := time.Date(2026, 3, 9, 15, 6, 29, 15*int(time.Millisecond), time.FixedZone("BRT", -3*60*60))
	got := PrettyTime(input).String()
	want := "03-09|18:06:29.015"

	if got != want {
		t.Fatalf("unexpected pretty time: got %q want %q", got, want)
	}
}
