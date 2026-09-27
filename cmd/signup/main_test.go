package main

import "testing"

func TestWantsVersion(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{nil, false},
		{[]string{"-version"}, true},
		{[]string{"--version"}, true},
		{[]string{"version"}, false},
		{[]string{"-version", "extra"}, false},
	}
	for _, c := range cases {
		if got := wantsVersion(c.args); got != c.want {
			t.Errorf("wantsVersion(%q) = %v, want %v", c.args, got, c.want)
		}
	}
}
