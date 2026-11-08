package main

import (
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/oschwald/geoip2-golang"
)

type countryLookup struct {
	db      *geoip2.Reader
	trusted []*net.IPNet
}

func openCountryLookup(path, proxies string) (*countryLookup, error) {
	if path == "" {
		if strings.TrimSpace(proxies) != "" {
			return nil, fmt.Errorf("PULSE_TRUSTED_PROXIES requires PULSE_GEOIP_DATABASE")
		}
		return nil, nil
	}
	db, err := geoip2.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open country database: %w", err)
	}
	c := &countryLookup{db: db}
	for _, raw := range strings.Split(proxies, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		_, network, err := net.ParseCIDR(raw)
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("invalid trusted proxy CIDR: %q", raw)
		}
		c.trusted = append(c.trusted, network)
	}
	return c, nil
}
func (c *countryLookup) isTrusted(ip net.IP) bool {
	for _, n := range c.trusted {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
func (c *countryLookup) clientIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer := net.ParseIP(host)
	if peer == nil || !c.isTrusted(peer) {
		return peer
	}
	parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	current := peer
	for i := len(parts) - 1; i >= 0; i-- {
		if !c.isTrusted(current) {
			break
		}
		candidate := net.ParseIP(strings.TrimSpace(parts[i]))
		if candidate == nil {
			return nil
		}
		current = candidate
	}
	return current
}
func (c *countryLookup) country(r *http.Request) string {
	if c == nil {
		return ""
	}
	ip := c.clientIP(r)
	if ip == nil || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return ""
	}
	record, err := c.db.Country(ip)
	if err != nil || record == nil {
		return ""
	}
	return record.Country.IsoCode
}
