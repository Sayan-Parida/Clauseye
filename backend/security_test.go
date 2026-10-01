package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Phase 0 regression tests. These cover the security fixes: client IP
// resolution, rate limiter bounds and fail-closed CORS.

// clearRateLimiterEnv is unnecessary for these: the limiter takes no env input.

func TestClientIPIgnoresForwardedHeaderByDefault(t *testing.T) {
	// With no trusted proxy configured, a client-supplied X-Forwarded-For must
	// be ignored, otherwise anyone can defeat per-IP limiting by setting a
	// random header.
	req := httptest.NewRequest(http.MethodPost, "/analyze", nil)
	req.RemoteAddr = "10.0.0.5:51000"
	req.Header.Set("X-Forwarded-For", "203.0.113.7")

	if got := clientIP(req, nil); got != "10.0.0.5" {
		t.Fatalf("clientIP = %q, want the RemoteAddr 10.0.0.5", got)
	}
}

func TestClientIPUsesForwardedHeaderFromTrustedProxy(t *testing.T) {
	proxies := parseTrustedProxies("10.0.0.0/8")
	req := httptest.NewRequest(http.MethodPost, "/analyze", nil)
	req.RemoteAddr = "10.0.0.5:51000"
	req.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.5")

	// Behind a trusted proxy, reading only RemoteAddr would collapse every
	// internet client onto the proxy address and share one rate-limit budget.
	if got := clientIP(req, proxies); got != "203.0.113.7" {
		t.Fatalf("clientIP = %q, want the left-most forwarded entry 203.0.113.7", got)
	}
}

func TestClientIPIgnoresForwardedHeaderFromUntrustedPeer(t *testing.T) {
	proxies := parseTrustedProxies("10.0.0.0/8")
	req := httptest.NewRequest(http.MethodPost, "/analyze", nil)
	req.RemoteAddr = "198.51.100.9:51000" // outside the trusted range
	req.Header.Set("X-Forwarded-For", "203.0.113.7")

	if got := clientIP(req, proxies); got != "198.51.100.9" {
		t.Fatalf("clientIP = %q, want the peer address for an untrusted source", got)
	}
}

func TestClientIPRejectsUnparseableForwardedEntry(t *testing.T) {
	proxies := parseTrustedProxies("10.0.0.0/8")
	req := httptest.NewRequest(http.MethodPost, "/analyze", nil)
	req.RemoteAddr = "10.0.0.5:51000"
	req.Header.Set("X-Forwarded-For", "not-an-ip")

	if got := clientIP(req, proxies); got != "10.0.0.5" {
		t.Fatalf("clientIP = %q, want a fallback to the peer address", got)
	}
}

func TestClientIPHandlesMissingPort(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/analyze", nil)
	req.RemoteAddr = "203.0.113.7"
	if got := clientIP(req, nil); got != "203.0.113.7" {
		t.Fatalf("clientIP = %q, want 203.0.113.7", got)
	}
}

func TestParseTrustedProxies(t *testing.T) {
	cases := []struct {
		in    string
		count int
	}{
		{"", 0},
		{"   ", 0},
		{"10.0.0.0/8", 1},
		{"10.0.0.0/8, 192.168.0.0/16", 2},
		{"10.0.0.5", 1},      // bare IPv4
		{"2001:db8::/32", 1}, // bare IPv6 CIDR
		{"not-a-cidr", 0},    // ignored, not fatal
		{"10.0.0.0/8, junk", 1},
	}
	for _, c := range cases {
		got := parseTrustedProxies(c.in)
		if len(got) != c.count {
			t.Errorf("parseTrustedProxies(%q) returned %d networks, want %d", c.in, len(got), c.count)
		}
	}
}

func TestParseTrustedProxiesBareIPv4IsNotWidenedToAllAddresses(t *testing.T) {
	// A bare address must not become a /0, which would make every peer trusted
	// and re-open the header-spoofing hole the allowlist exists to close.
	networks := parseTrustedProxies("10.0.0.5")
	if len(networks) != 1 {
		t.Fatalf("expected 1 network, got %d", len(networks))
	}
	req := httptest.NewRequest(http.MethodPost, "/analyze", nil)
	req.RemoteAddr = "10.0.0.9:51000"
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	if got := clientIP(req, networks); got != "10.0.0.9" {
		t.Fatalf("a bare trusted address was treated as a /0: clientIP = %q", got)
	}
}

func TestRateLimiterEnforcesItsBudget(t *testing.T) {
	limiter := newRateLimiter()
	for i := 0; i < rateLimit; i++ {
		if !limiter.allow("198.51.100.1") {
			t.Fatalf("request %d was rejected within the budget of %d", i+1, rateLimit)
		}
	}
	if limiter.allow("198.51.100.1") {
		t.Fatal("a request past the budget was allowed")
	}
	// A different client must have its own budget.
	if !limiter.allow("198.51.100.2") {
		t.Fatal("an unrelated client was rate limited")
	}
}

func TestRateLimiterEvictsStaleWindows(t *testing.T) {
	limiter := newRateLimiter()
	limiter.allow("198.51.100.1")
	limiter.allow("198.51.100.2")

	// Age both windows past the window without waiting a real minute.
	limiter.mu.Lock()
	for ip, w := range limiter.clients {
		w.started = w.started.Add(-2 * rateWindow)
		limiter.clients[ip] = w
	}
	sizeBefore := len(limiter.clients)
	limiter.mu.Unlock()

	limiter.allow("198.51.100.3")

	limiter.mu.Lock()
	sizeAfter := len(limiter.clients)
	limiter.mu.Unlock()

	if sizeAfter > sizeBefore {
		t.Fatalf("stale windows were not evicted: %d before, %d after", sizeBefore, sizeAfter)
	}
	if sizeAfter != 1 {
		t.Fatalf("expected only the new client to remain, got %d", sizeAfter)
	}
}

func TestRateLimiterBoundsTheClientMap(t *testing.T) {
	limiter := newRateLimiter()
	// Fill past the bound with windows that are all still fresh, so eviction by
	// age cannot clean them up.
	limiter.mu.Lock()
	now := time.Now()
	for i := 0; i < maxTrackedClients+10; i++ {
		limiter.clients[strings.Repeat("a", 8)+itoa(i)] = clientWindow{started: now, count: 1}
	}
	limiter.mu.Unlock()

	limiter.allow("trigger")

	limiter.mu.Lock()
	size := len(limiter.clients)
	limiter.mu.Unlock()

	if size > maxTrackedClients+2 {
		t.Fatalf("client map grew to %d, past the %d bound", size, maxTrackedClients)
	}
}

func TestDailyCapEnforcesAndResets(t *testing.T) {
	cap := newDailyCap(3)
	for i := 0; i < 3; i++ {
		if !cap.allow() {
			t.Fatalf("call %d was rejected within the cap", i+1)
		}
	}
	if cap.allow() {
		t.Fatal("a call past the cap was allowed")
	}

	cap.mu.Lock()
	cap.day = cap.day.Add(-25 * time.Hour)
	cap.mu.Unlock()

	if !cap.allow() {
		t.Fatal("the cap did not reset after the window elapsed")
	}
}

func TestWithCORSFailsClosedWhenUnconfigured(t *testing.T) {
	handler := withCORS("", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodOptions, "/analyze", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want it absent when unconfigured", got)
	}
}

func TestWithCORSEchoesConfiguredOrigin(t *testing.T) {
	const origin = "https://clause.example"
	handler := withCORS(origin, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodOptions, "/analyze", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != origin {
		t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, origin)
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want %d", rec.Code, http.StatusNoContent)
	}
}

func TestRateLimiterIsConcurrencySafe(t *testing.T) {
	// Run under -race this exercises the map access that previously had no
	// eviction and therefore no bound.
	limiter := newRateLimiter()
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				limiter.allow(itoa(n) + "." + itoa(j))
			}
		}(i)
	}
	wg.Wait()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}
