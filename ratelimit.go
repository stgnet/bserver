package main

import (
	"log"
	"math/rand"
	"net"
	"net/http"
	"sync"
	"time"
)

// clientIP extracts the client's IP address from the request's RemoteAddr.
func clientIP(r *http.Request) string {
	addr := r.RemoteAddr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

// ipEntry tracks error counts and block status for a single IP address.
type ipEntry struct {
	consecutiveErrors int
	blockedUntil      time.Time
	penaltyCount      int // number of times blocked; used for escalating penalty
	lastSeen          time.Time
	dropLogged        bool // true after the first dropped request has been logged
}

// rateLimiter tracks per-IP consecutive error counts and blocks IPs that
// make too many failed requests in a row, protecting the server from
// scanning and fishing attacks.
type rateLimiter struct {
	mu      sync.Mutex
	entries map[string]*ipEntry
	done    chan struct{}
}

const (
	maxConsecutiveErrors = 10               // errors in a row before blocking
	basePenaltyDuration  = 10 * time.Minute // first block duration
	maxPenaltyLevel      = 8               // cap doublings (max ~42 hours)
	cleanupInterval      = 5 * time.Minute
	entryExpiry          = 1 * time.Hour // remove idle entries after this
)

func newRateLimiter() *rateLimiter {
	rl := &rateLimiter{
		entries: make(map[string]*ipEntry),
		done:    make(chan struct{}),
	}
	go rl.cleanupLoop()
	return rl
}

// Close stops the background cleanup goroutine.
func (rl *rateLimiter) Close() {
	close(rl.done)
}

func (rl *rateLimiter) cleanupLoop() {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			rl.cleanup()
		case <-rl.done:
			return
		}
	}
}

// cleanup removes entries for IPs that are no longer blocked and haven't
// been seen recently, preventing unbounded memory growth.
func (rl *rateLimiter) cleanup() {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	for ip, entry := range rl.entries {
		if now.After(entry.blockedUntil) && now.Sub(entry.lastSeen) > entryExpiry {
			delete(rl.entries, ip)
		}
	}
}

// isBlocked checks if the given IP is currently rate-limited.
// It updates lastSeen on blocked IPs to track ongoing attack activity.
// Returns (blocked, firstDrop) where firstDrop is true only on the
// first blocked request (for logging the drop once without flooding).
func (rl *rateLimiter) isBlocked(ip string) (blocked, firstDrop bool) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	entry, ok := rl.entries[ip]
	if !ok {
		return false, false
	}
	if time.Now().Before(entry.blockedUntil) {
		entry.lastSeen = time.Now()
		first := !entry.dropLogged
		entry.dropLogged = true
		return true, first
	}
	return false, false
}

// recordResult records a response status code for the given IP.
// Error responses (status >= 400) increment the consecutive error count.
// Successful responses reset it. When the count reaches the threshold,
// the IP is blocked with an escalating penalty.
// Returns the block penalty duration if the IP was just blocked, or zero.
func (rl *rateLimiter) recordResult(ip string, statusCode int) time.Duration {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	entry, ok := rl.entries[ip]
	if !ok {
		entry = &ipEntry{}
		rl.entries[ip] = entry
	}
	entry.lastSeen = now

	if statusCode >= 400 {
		entry.consecutiveErrors++
		if entry.consecutiveErrors >= maxConsecutiveErrors {
			penalty := calcPenalty(entry.penaltyCount)
			entry.blockedUntil = now.Add(penalty)
			entry.penaltyCount++
			entry.consecutiveErrors = 0
			entry.dropLogged = false
			return penalty
		}
	} else {
		// Successful response: reset error count but preserve penalty history
		entry.consecutiveErrors = 0
	}
	return 0
}

// calcPenalty returns the block duration for the given penalty level.
// Each level doubles the base duration, capped at maxPenaltyLevel doublings.
func calcPenalty(level int) time.Duration {
	penalty := basePenaltyDuration
	n := level
	if n > maxPenaltyLevel {
		n = maxPenaltyLevel
	}
	for i := 0; i < n; i++ {
		penalty *= 2
	}
	return penalty
}

// dropResponse handles a request from a blocked IP with minimal server
// overhead. It uses randomized strategies to confuse and slow down
// automated scanners: closing the connection, returning bare error codes,
// or delaying briefly before closing.
func dropResponse(w http.ResponseWriter, r *http.Request) {
	switch rand.Intn(4) {
	case 0:
		// Close connection immediately (lowest possible overhead)
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				conn.Close()
				return
			}
		}
		w.WriteHeader(http.StatusTooManyRequests)
	case 1:
		// 429 Too Many Requests — bare response, no body
		w.WriteHeader(http.StatusTooManyRequests)
	case 2:
		// 503 Service Unavailable — bare response, no body
		w.WriteHeader(http.StatusServiceUnavailable)
	case 3:
		// Brief delay then close — wastes attacker's time
		time.Sleep(time.Duration(1+rand.Intn(3)) * time.Second)
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				conn.Close()
				return
			}
		}
		w.WriteHeader(http.StatusTooManyRequests)
	}
}

// rateLimitMiddleware wraps a handler with per-IP rate limiting based on
// consecutive error responses. IPs that exceed the threshold are blocked
// and receive minimal drop responses.
func rateLimitMiddleware(rl *rateLimiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)

		if blocked, firstDrop := rl.isBlocked(ip); blocked {
			if firstDrop {
				log.Printf("%s %s %s %s dropped", ip, r.Host, r.Method, r.URL.Path)
			}
			dropResponse(w, r)
			return
		}

		lrw := &loggingResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(lrw, r)

		if penalty := rl.recordResult(ip, lrw.statusCode); penalty > 0 {
			log.Printf("%s rate-limited after %d consecutive errors (penalty: %s)",
				ip, maxConsecutiveErrors, penalty)
		}
	})
}
