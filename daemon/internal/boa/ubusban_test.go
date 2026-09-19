package boa

import (
	"testing"
	"time"
)

func TestUbusBanArg(t *testing.T) {
	cases := []struct {
		name string
		hold time.Duration
		want string
		ok   bool
	}{
		{"capped at 15s", 60 * time.Second,
			`{"addr":"aa:bb:cc:dd:ee:ff","deauth":false,"reason":5,"ban_time":15000}`, true},
		// A mirror that outlived a 10s deadzone would lengthen the outage
		// being measured.
		{"never longer than the hold", 10 * time.Second,
			`{"addr":"aa:bb:cc:dd:ee:ff","deauth":false,"reason":5,"ban_time":10000}`, true},
		{"a restored deadzone's remainder", 2500 * time.Millisecond,
			`{"addr":"aa:bb:cc:dd:ee:ff","deauth":false,"reason":5,"ban_time":2500}`, true},
		{"under a second is not worth placing", 400 * time.Millisecond, "", false},
		{"an expired hold places nothing", 0, "", false},
	}
	for _, c := range cases {
		got, ok := ubusBanArg("aa:bb:cc:dd:ee:ff", c.hold)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: got %s, %v; want %s, %v", c.name, got, ok, c.want, c.ok)
		}
	}
}
