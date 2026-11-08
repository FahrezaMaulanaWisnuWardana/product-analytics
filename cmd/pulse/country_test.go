package main

import (
	"net"
	"net/http/httptest"
	"testing"
)

func TestCountryClientIPTrustedProxy(t *testing.T) {
	_, network, _ := net.ParseCIDR("10.0.0.0/8")
	c := &countryLookup{trusted: []*net.IPNet{network}}
	tests := []struct{ peer, forwarded, want string }{
		{"203.0.113.9:42", "198.51.100.10", "203.0.113.9"},
		{"10.0.0.2:42", "198.51.100.10, 10.0.0.3", "198.51.100.10"},
		{"10.0.0.2:42", "192.0.2.7, 198.51.100.10", "198.51.100.10"},
		{"10.0.0.2:42", "not-an-ip", ""},
	}
	for _, tc := range tests {
		r := httptest.NewRequest("POST", "/capture", nil)
		r.RemoteAddr = tc.peer
		r.Header.Set("X-Forwarded-For", tc.forwarded)
		got := c.clientIP(r)
		if tc.want == "" {
			if got != nil {
				t.Fatalf("%s: got %s", tc.peer, got)
			}
			continue
		}
		if got.String() != tc.want {
			t.Fatalf("%s: got %s want %s", tc.peer, got, tc.want)
		}
	}
}

func TestCountryLookupDisabledAndInvalidConfig(t *testing.T) {
	var c *countryLookup
	if got := c.country(httptest.NewRequest("POST", "/capture", nil)); got != "" {
		t.Fatal(got)
	}
	if _, err := openCountryLookup("", "10.0.0.0/8"); err == nil {
		t.Fatal("trusted proxies without a database accepted")
	}
}
