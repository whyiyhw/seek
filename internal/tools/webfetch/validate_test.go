package webfetch

import (
	"errors"
	"net"
	"strings"
	"testing"
)

// --- ValidateRequestURL: scheme / host / port ------------------------

func TestValidateRequestURL_AllowsHTTPSWithStandardPorts(t *testing.T) {
	t.Parallel()
	cases := []string{
		"https://example.com",
		"https://example.com/",
		"https://example.com/path/to/doc",
		"https://api.example.com:443",
		"https://docs.example.com:8443/api",
		"https://example.com:8080/page",
		"https://example.com:80/page", // 80 is in default allowlist
		"https://example.com/page?q=1&x=2#frag",
		"https://example.com/path%20with%20spaces",
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			u, err := ValidateRequestURL(raw, DefaultOptions())
			if err != nil {
				t.Fatalf("expected pass, got %v", err)
			}
			if u == nil {
				t.Fatal("nil URL on pass")
			}
		})
	}
}

func TestValidateRequestURL_RejectsHTTPByDefault(t *testing.T) {
	t.Parallel()
	_, err := ValidateRequestURL("http://example.com", DefaultOptions())
	if !errors.Is(err, ErrBlockedScheme) {
		t.Fatalf("err = %v, want ErrBlockedScheme", err)
	}
	if !strings.Contains(err.Error(), "SEEK_WEBFETCH_ALLOW_HTTP") {
		t.Errorf("error should mention the env override, got: %v", err)
	}
}

func TestValidateRequestURL_AllowsHTTPWhenOptIn(t *testing.T) {
	t.Parallel()
	opts := DefaultOptions()
	opts.AllowHTTP = true
	if _, err := ValidateRequestURL("http://example.com", opts); err != nil {
		t.Fatalf("AllowHTTP=true should accept http://, got %v", err)
	}
}

func TestValidateRequestURL_RejectsNonHTTPSchemes(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		"file:///etc/passwd",
		"file://localhost/etc/passwd",
		"ftp://ftp.example.com/file",
		"gopher://example.com/",
		"data:text/plain,hello",
		"javascript:alert(1)",
		"chrome-extension://abc",
		"about:blank",
	} {
		t.Run(raw, func(t *testing.T) {
			_, err := ValidateRequestURL(raw, DefaultOptions())
			if !errors.Is(err, ErrBlockedScheme) && !errors.Is(err, ErrInvalidURL) {
				t.Errorf("err = %v, want ErrBlockedScheme or ErrInvalidURL", err)
			}
		})
	}
}

func TestValidateRequestURL_RejectsRelativeURLs(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		"/path",
		"path/only",
		"//example.com/path", // protocol-relative; no scheme
		"",
	} {
		t.Run(raw, func(t *testing.T) {
			_, err := ValidateRequestURL(raw, DefaultOptions())
			if !errors.Is(err, ErrInvalidURL) {
				t.Errorf("err = %v, want ErrInvalidURL", err)
			}
		})
	}
}

func TestValidateRequestURL_RejectsLocalhostVariants(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		"https://localhost/",
		"https://LOCALHOST/",
		"https://LocalHost:443/",
		"https://api.localhost/",
		"https://foo.bar.localhost/path",
		"https://localhost.", // trailing dot (FQDN form)
	} {
		t.Run(raw, func(t *testing.T) {
			_, err := ValidateRequestURL(raw, DefaultOptions())
			if !errors.Is(err, ErrBlockedHost) {
				t.Errorf("err = %v, want ErrBlockedHost", err)
			}
		})
	}
}

func TestValidateRequestURL_RejectsWildcardBindHosts(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		"https://0.0.0.0/",
		"https://0/",
	} {
		t.Run(raw, func(t *testing.T) {
			_, err := ValidateRequestURL(raw, DefaultOptions())
			if !errors.Is(err, ErrBlockedHost) {
				t.Errorf("err = %v, want ErrBlockedHost", err)
			}
		})
	}
}

func TestValidateRequestURL_RejectsNonStandardPorts(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		"https://example.com:22",    // ssh
		"https://example.com:3306",  // mysql
		"https://example.com:5432",  // postgres
		"https://example.com:6379",  // redis
		"https://example.com:11211", // memcached
		"https://example.com:9000",  // misc internal
	} {
		t.Run(raw, func(t *testing.T) {
			_, err := ValidateRequestURL(raw, DefaultOptions())
			if !errors.Is(err, ErrBlockedPort) {
				t.Errorf("err = %v, want ErrBlockedPort", err)
			}
		})
	}
}

func TestValidateRequestURL_PortAllowlistOverride(t *testing.T) {
	t.Parallel()
	// Custom allowlist: only 443. 8443 (a default) should now reject.
	opts := ValidationOptions{AllowedPorts: []int{443}}
	if _, err := ValidateRequestURL("https://example.com:443", opts); err != nil {
		t.Errorf("443 should still pass under tight allowlist, got %v", err)
	}
	if _, err := ValidateRequestURL("https://example.com:8443", opts); !errors.Is(err, ErrBlockedPort) {
		t.Errorf("8443 should be blocked under {443}-only allowlist, got %v", err)
	}
}

// --- ValidateIP: SSRF classification ---------------------------------

func TestValidateIP_AllowsPublicAddresses(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		"8.8.8.8",                            // Google DNS
		"1.1.1.1",                            // Cloudflare DNS
		"93.184.216.34",                      // example.com
		"2606:2800:220:1:248:1893:25c8:1946", // example.com IPv6
		"2001:4860:4860::8888",               // Google DNS IPv6
	} {
		t.Run(raw, func(t *testing.T) {
			if err := ValidateIP(net.ParseIP(raw)); err != nil {
				t.Errorf("public IP %s rejected: %v", raw, err)
			}
		})
	}
}

func TestValidateIP_RejectsLoopback(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		"127.0.0.1",
		"127.255.255.254",
		"::1",
		"::ffff:127.0.0.1", // IPv4-mapped IPv6 loopback
	} {
		t.Run(raw, func(t *testing.T) {
			err := ValidateIP(net.ParseIP(raw))
			if !errors.Is(err, ErrBlockedTarget) {
				t.Errorf("err = %v, want ErrBlockedTarget", err)
			}
		})
	}
}

func TestValidateIP_RejectsRFC1918Private(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		"10.0.0.1",
		"10.255.255.255",
		"172.16.0.1",
		"172.31.255.255",
		"192.168.0.1",
		"192.168.255.255",
		"fc00::1",      // ULA
		"fdff:ffff::1", // ULA upper bound
	} {
		t.Run(raw, func(t *testing.T) {
			err := ValidateIP(net.ParseIP(raw))
			if !errors.Is(err, ErrBlockedTarget) {
				t.Errorf("err = %v, want ErrBlockedTarget", err)
			}
		})
	}
}

func TestValidateIP_RejectsLinkLocal(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		"169.254.0.1",
		"169.254.169.254", // notorious cloud metadata service address
		"fe80::1",
	} {
		t.Run(raw, func(t *testing.T) {
			err := ValidateIP(net.ParseIP(raw))
			if !errors.Is(err, ErrBlockedTarget) {
				t.Errorf("err = %v, want ErrBlockedTarget", err)
			}
		})
	}
}

func TestValidateIP_RejectsCGNAT(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		"100.64.0.1",
		"100.127.255.254",
	} {
		t.Run(raw, func(t *testing.T) {
			err := ValidateIP(net.ParseIP(raw))
			if !errors.Is(err, ErrBlockedTarget) {
				t.Errorf("err = %v, want ErrBlockedTarget", err)
			}
		})
	}
}

func TestValidateIP_AllowsAdjacentToCGNAT(t *testing.T) {
	t.Parallel()
	// CGNAT is 100.64/10 — make sure we don't over-block adjacent
	// public space.
	for _, raw := range []string{
		"100.63.255.255", // just below CGNAT
		"100.128.0.0",    // just above CGNAT
	} {
		t.Run(raw, func(t *testing.T) {
			if err := ValidateIP(net.ParseIP(raw)); err != nil {
				t.Errorf("adjacent-to-CGNAT %s incorrectly blocked: %v", raw, err)
			}
		})
	}
}

func TestValidateIP_RejectsMulticast(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		"224.0.0.1",       // mDNS
		"239.255.255.250", // SSDP
		"ff02::1",         // IPv6 all-nodes multicast
	} {
		t.Run(raw, func(t *testing.T) {
			err := ValidateIP(net.ParseIP(raw))
			if !errors.Is(err, ErrBlockedTarget) {
				t.Errorf("err = %v, want ErrBlockedTarget", err)
			}
		})
	}
}

func TestValidateIP_RejectsUnspecifiedAndBroadcast(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		"0.0.0.0",
		"::",
		"255.255.255.255",
	} {
		t.Run(raw, func(t *testing.T) {
			err := ValidateIP(net.ParseIP(raw))
			if !errors.Is(err, ErrBlockedTarget) {
				t.Errorf("err = %v, want ErrBlockedTarget", err)
			}
		})
	}
}

func TestValidateIP_RejectsNil(t *testing.T) {
	t.Parallel()
	if err := ValidateIP(nil); !errors.Is(err, ErrBlockedTarget) {
		t.Errorf("nil IP should be ErrBlockedTarget, got %v", err)
	}
}

// --- Defense-in-depth: DNS-rebinding pattern --------------------------
// A hostname that PASSES ValidateRequestURL (no obvious junk in the
// name) but RESOLVES to a private IP — must be caught at ValidateIP
// time. ValidateRequestURL doesn't do DNS; the Tool's DialContext
// must call ValidateIP after the lookup.

func TestSSRF_AttackerControlledNameResolvingPrivate(t *testing.T) {
	t.Parallel()
	// The URL itself passes: it's https://, the hostname has nothing
	// obviously wrong about it.
	_, err := ValidateRequestURL("https://evil.example.com/", DefaultOptions())
	if err != nil {
		t.Fatalf("benign-looking URL rejected at parse time: %v", err)
	}

	// But once "DNS" returns a private IP, ValidateIP must reject.
	resolved := net.ParseIP("10.0.0.1")
	if err := ValidateIP(resolved); !errors.Is(err, ErrBlockedTarget) {
		t.Fatalf("DNS-resolved private IP must be blocked at the IP layer; got %v", err)
	}
}
