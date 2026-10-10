package main

import "testing"

func TestIPAllowlist(t *testing.T) {
	// Empty list = feature disabled = allow everything.
	if !ipAllowed("8.8.8.8", nil) {
		t.Error("empty allowlist should allow all")
	}
	nets := parseIPNets([]string{"203.0.113.51", "10.0.0.0/8", "::1", "garbage", "999.1.1.1"})
	cases := map[string]bool{
		"203.0.113.51": true,  // exact IPv4
		"203.0.113.52": false, // neighbor not allowed
		"10.5.6.7":     true,  // inside CIDR
		"11.0.0.1":     false, // outside CIDR
		"::1":          true,  // IPv6 loopback
		"8.8.8.8":      false, // not listed
		"":             false, // unparseable -> denied
	}
	for ip, want := range cases {
		if got := ipAllowed(ip, nets); got != want {
			t.Errorf("ipAllowed(%q) = %v, want %v", ip, got, want)
		}
	}
	// Invalid entries ("garbage","999.1.1.1") must be skipped, leaving 3 valid.
	if len(nets) != 3 {
		t.Errorf("parseIPNets kept %d entries, want 3 (invalids skipped)", len(nets))
	}
}
