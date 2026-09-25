// Package linkedin is a cookie-based client for LinkedIn's internal Voyager API.
package linkedin

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"regexp"
	"strings"
	"time"
)

const BaseURL = "https://www.linkedin.com"

const DefaultUA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"

var (
	ErrAuth      = errors.New("session invalid: li_at / JSESSIONID may have expired")
	ErrRateLimit = errors.New("linkedin rate limit (429)")
)

type Client struct {
	HTTP     *http.Client
	LiAt     string
	JSession string
	UA       string
	Lang     string
	Retries  int
	Debug    bool
	meID     string
}

func New(liAt, jsession, ua, lang string) (*Client, error) {
	liAt = strings.TrimSpace(liAt)
	jsession = strings.Trim(strings.TrimSpace(jsession), `"`)
	if liAt == "" || jsession == "" {
		return nil, errors.New("li_at and JSESSIONID are required (LI_AT / LI_JSESSIONID)")
	}
	if ua == "" {
		ua = DefaultUA
	}
	if lang == "" {
		lang = "tr_TR"
	}
	jar, _ := cookiejar.New(nil)
	return &Client{
		HTTP:     &http.Client{Timeout: 45 * time.Second, Jar: jar},
		LiAt:     liAt,
		JSession: jsession,
		UA:       ua,
		Lang:     lang,
		Retries:  3,
	}, nil
}

func (c *Client) Do(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	url := path
	if strings.HasPrefix(path, "/") {
		url = BaseURL + path
	}
	var lastErr error
	for attempt := 0; attempt <= c.Retries; attempt++ {
		if attempt > 0 {
			wait := time.Duration(1<<attempt) * 15 * time.Second
			if c.Debug {
				fmt.Printf("  [retry %d] %v bekleniyor (%v)\n", attempt, wait, lastErr)
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
		}
		var rdr io.Reader
		if body != nil {
			rdr = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, url, rdr)
		if err != nil {
			return nil, err
		}
		req.Header.Set("cookie", fmt.Sprintf(`li_at=%s; JSESSIONID="%s"`, c.LiAt, c.JSession))
		req.Header.Set("csrf-token", c.JSession)
		req.Header.Set("x-restli-protocol-version", "2.0.0")
		req.Header.Set("x-li-lang", c.Lang)
		req.Header.Set("x-li-track", `{"clientVersion":"1.13.0","mpVersion":"1.13.0","osName":"web","timezoneOffset":3,"mpName":"voyager-web","displayDensity":1,"displayWidth":1920,"displayHeight":1080,"deviceFormFactor":"DESKTOP"}`)
		req.Header.Set("x-li-page-instance", "urn:li:page:d_flagship3_search_srp_people;"+randomToken())
		req.Header.Set("x-li-pem-metadata", "Voyager - People SRP=search-results")
		req.Header.Set("accept", "application/vnd.linkedin.normalized+json+2.1")
		req.Header.Set("accept-language", "tr-TR,tr;q=0.9,en;q=0.8")
		req.Header.Set("user-agent", c.UA)
		req.Header.Set("referer", BaseURL+"/feed/")
		if body != nil {
			req.Header.Set("content-type", "application/json; charset=UTF-8")
		}

		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		b, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		switch {
		case resp.StatusCode == 401, resp.StatusCode == 403:
			return nil, fmt.Errorf("%w (HTTP %d)", ErrAuth, resp.StatusCode)
		case resp.StatusCode == 429:
			lastErr = ErrRateLimit
			continue
		case resp.StatusCode >= 500:
			lastErr = fmt.Errorf("server error HTTP %d", resp.StatusCode)
			continue
		case resp.StatusCode >= 400:
			return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, snippet(b))
		}
		return b, nil
	}
	return nil, lastErr
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

var reProfileURN = regexp.MustCompile(`urn:li:fs(?:d)?_(?:mini)?[Pp]rofile:\(?([A-Za-z0-9_\-]{8,})`)

// ExtractURNID returns the first fsd_profile id found in a response body (ACoAA…).
func ExtractURNID(b []byte) string {
	if m := reProfileURN.FindSubmatch(b); m != nil {
		return string(m[1])
	}
	return ""
}

// Me returns the signed-in user's profile id.
func (c *Client) Me(ctx context.Context) (string, error) {
	if c.meID != "" {
		return c.meID, nil
	}
	b, err := c.Do(ctx, http.MethodGet, "/voyager/api/me", nil)
	if err != nil {
		return "", err
	}
	id := ExtractURNID(b)
	if id == "" {
		return "", errors.New("could not read own profile id")
	}
	c.meID = id
	return id, nil
}

// MeInfo returns the signed-in user's id, display name and public profile handle.
func (c *Client) MeInfo(ctx context.Context) (id, name, publicID string, err error) {
	b, err := c.Do(ctx, http.MethodGet, "/voyager/api/me", nil)
	if err != nil {
		return "", "", "", err
	}
	id = ExtractURNID(b)
	if id == "" {
		return "", "", "", errors.New("profile id not found")
	}
	c.meID = id
	var root any
	if json.Unmarshal(b, &root) == nil {
		walk(root, func(m map[string]any) {
			fn, ok1 := m["firstName"].(string)
			ln, ok2 := m["lastName"].(string)
			if ok1 && ok2 && name == "" {
				name = strings.TrimSpace(fn + " " + ln)
			}
			if p, ok := m["publicIdentifier"].(string); ok && publicID == "" {
				publicID = p
			}
		})
	}
	return id, name, publicID, nil
}

// ResolveURN maps a publicIdentifier to an fsd_profile id.
func (c *Client) ResolveURN(ctx context.Context, publicID string) (string, error) {
	publicID = strings.Trim(publicID, "/")
	paths := []string{
		"/voyager/api/identity/dash/profiles?q=memberIdentity&memberIdentity=" + publicID,
		"/voyager/api/identity/profiles/" + publicID + "/profileView",
		"/in/" + publicID + "/",
	}
	var lastErr error
	for _, p := range paths {
		b, err := c.Do(ctx, http.MethodGet, p, nil)
		if err != nil {
			lastErr = err
			if errors.Is(err, ErrAuth) {
				return "", err
			}
			continue
		}
		if id := ExtractURNID(b); id != "" {
			return id, nil
		}
	}
	if lastErr != nil {
		return "", fmt.Errorf("could not resolve urn for %s: %w", publicID, lastErr)
	}
	return "", fmt.Errorf("no urn found for %s", publicID)
}

func randomToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func uuid4() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b)
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])
}
