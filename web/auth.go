package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	cookieName    = "postit_session"
	sessionMaxAge = 30 * 24 * time.Hour
	loginWindow   = time.Minute
	loginAttempts = 5 // failed logins per IP and window
)

// auth checks the login password and signs session cookies. The signing key
// depends on the password and the sync token, so changing either ends every
// session.
type auth struct {
	password []byte
	key      []byte

	mu     sync.Mutex
	failed map[string][]time.Time
}

func newAuth(password, token string) *auth {
	k := sha256.Sum256([]byte("postit-web session\x00" + password + "\x00" + token))
	return &auth{password: []byte(password), key: k[:], failed: map[string][]time.Time{}}
}

func (a *auth) checkPassword(pw string) bool {
	// Compare hashes so the comparison does not leak the length.
	got, want := sha256.Sum256([]byte(pw)), sha256.Sum256(a.password)
	return subtle.ConstantTimeCompare(got[:], want[:]) == 1
}

func (a *auth) sign(exp int64) string {
	m := hmac.New(sha256.New, a.key)
	m.Write([]byte(strconv.FormatInt(exp, 10)))
	return hex.EncodeToString(m.Sum(nil))
}

func (a *auth) newSession(now time.Time) string {
	exp := now.Add(sessionMaxAge).Unix()
	return strconv.FormatInt(exp, 10) + "." + a.sign(exp)
}

func (a *auth) validSession(v string, now time.Time) bool {
	expStr, mac, ok := strings.Cut(v, ".")
	if !ok {
		return false
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || now.Unix() > exp {
		return false
	}
	return hmac.Equal([]byte(mac), []byte(a.sign(exp)))
}

func (a *auth) loggedIn(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	return err == nil && a.validSession(c.Value, time.Now())
}

func (a *auth) setCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    a.newSession(time.Now()),
		Path:     "/",
		MaxAge:   int(sessionMaxAge / time.Second),
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})
}

func clearCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteLaxMode,
	})
}

// isHTTPS reports whether the browser talks HTTPS, directly or through the
// reverse proxy.
func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// tooManyFailures reports whether ip used up its failed logins.
func (a *auth) tooManyFailures(ip string, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.recent(ip, now)) >= loginAttempts
}

func (a *auth) recordFailure(ip string, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.failed[ip] = append(a.recent(ip, now), now)
	if len(a.failed) > 10000 { // keep the map bounded
		for k := range a.failed {
			if len(a.recent(k, now)) == 0 {
				delete(a.failed, k)
			}
		}
	}
}

// recent returns ip's failures inside the window; a.mu must be held.
func (a *auth) recent(ip string, now time.Time) []time.Time {
	ts := a.failed[ip]
	i := 0
	for i < len(ts) && now.Sub(ts[i]) > loginWindow {
		i++
	}
	ts = ts[i:]
	if len(ts) == 0 {
		delete(a.failed, ip)
		return nil
	}
	a.failed[ip] = ts
	return ts
}

// sameOrigin reports whether a state-changing request comes from a page of
// this app (CSRF protection on top of the SameSite cookie).
func sameOrigin(r *http.Request) bool {
	src := r.Header.Get("Origin")
	if src == "" || src == "null" {
		src = r.Header.Get("Referer")
	}
	if src == "" {
		return false
	}
	u, err := url.Parse(src)
	if err != nil || u.Host == "" {
		return false
	}
	host := r.Host
	if fh := r.Header.Get("X-Forwarded-Host"); fh != "" {
		host, _, _ = strings.Cut(fh, ",")
		host = strings.TrimSpace(host)
	}
	return strings.EqualFold(u.Host, host)
}

// clientIP prefers the address the reverse proxy reports. It takes the last
// X-Forwarded-For entry: that one is added by the proxy, earlier ones come
// from the client and could be forged to dodge the login limit.
func clientIP(r *http.Request) string {
	if f := r.Header.Get("X-Forwarded-For"); f != "" {
		parts := strings.Split(f, ",")
		return strings.TrimSpace(parts[len(parts)-1])
	}
	host := r.RemoteAddr
	if i := strings.LastIndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	return host
}
