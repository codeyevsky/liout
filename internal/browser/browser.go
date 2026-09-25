// Package browser drives a real Firefox/Chromium session through Playwright so
// liout can reuse the user's own LinkedIn login and capture its cookies ·
// no manual copy-paste, no password. Modeled on the githubFlex browser flow.
package browser

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/mxschmitt/playwright-go"
)

const loginURL = "https://www.linkedin.com/login"
const feedURL = "https://www.linkedin.com/feed/"

// chromePaths: real Google Chrome first, then Chromium/Brave as fallback.
var chromePaths = []string{
	"/usr/bin/google-chrome-stable",
	"/usr/bin/google-chrome",
	"/opt/google/chrome/chrome",
	"/usr/bin/chromium",
	"/usr/bin/chromium-browser",
	"/usr/bin/brave",
}

// chromeExe returns the path to the preferred Chrome-family binary.
func chromeExe() string {
	for _, p := range chromePaths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// Creds are the two cookies liout needs, plus who they belong to.
type Creds struct {
	LiAt     string
	JSession string
	Name     string
	PublicID string
}

func (c Creds) OK() bool { return c.LiAt != "" && c.JSession != "" }

type Session struct {
	pw   *playwright.Playwright
	ctx  playwright.BrowserContext
	Page playwright.Page
}

func (s *Session) Close() {
	if s == nil {
		return
	}
	if s.ctx != nil {
		_ = s.ctx.Close()
	}
	if s.pw != nil {
		_ = s.pw.Stop()
	}
}

// profileDir keeps a persistent liout browser profile so a login sticks
// between runs · the second time, sign-in is instant.
func profileDir(browser string) string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "share")
	}
	p := filepath.Join(base, "liout", "profiles", browser)
	_ = os.MkdirAll(p, 0o755)
	return p
}

// clearProfileLocks removes Chromium/Chrome singleton lock files left behind
// when a previous run crashed, so we can reopen the profile without manual cleanup.
func clearProfileLocks(dir string) {
	for _, name := range []string{"SingletonLock", "SingletonCookie", "SingletonSocket", "lockfile", ".parentlock"} {
		_ = os.Remove(filepath.Join(dir, name))
	}
}

// EnsureDriver installs the Playwright browsers if they're missing.
func EnsureDriver(browser string) error {
	run := &playwright.RunOptions{Verbose: false}
	switch browser {
	case "firefox":
		run.Browsers = []string{"firefox"}
	default:
		run.Browsers = []string{"chromium"}
	}
	return playwright.Install(run)
}

type Opts struct {
	Browser  string // firefox | chromium
	Headless bool
}

// Open launches a persistent browser context at LinkedIn.
func Open(o Opts) (*Session, error) {
	if o.Browser == "" {
		o.Browser = DefaultBrowser()
	}
	pw, err := playwright.Run()
	if err != nil {
		if EnsureDriver(o.Browser) == nil {
			pw, err = playwright.Run()
		}
		if err != nil {
			return nil, fmt.Errorf("playwright not ready: %w", err)
		}
	}

	var engine playwright.BrowserType
	switch o.Browser {
	case "firefox":
		engine = pw.Firefox
	case "chrome", "chromium":
		engine = pw.Chromium
	default:
		_ = pw.Stop()
		return nil, fmt.Errorf("unknown browser %q (use chrome or firefox)", o.Browser)
	}

	opts := playwright.BrowserTypeLaunchPersistentContextOptions{
		Headless: playwright.Bool(o.Headless),
		Viewport: &playwright.Size{Width: 1360, Height: 900},
	}
	if o.Browser != "firefox" {
		opts.Args = []string{"--disable-blink-features=AutomationControlled"}
		opts.Channel = playwright.String("chrome") // prefer real Chrome
		if exe := chromeExe(); exe != "" {
			opts.ExecutablePath = playwright.String(exe)
			opts.Channel = nil // explicit path wins over channel
		}
	}

	dir := profileDir(o.Browser)
	ctx, err := engine.LaunchPersistentContext(dir, opts)
	if err != nil && regexp.MustCompile(`(?i)SingletonLock|ProcessSingleton|in use|already running|Failed to (create|launch)`).MatchString(err.Error()) {
		// stale lock from a previous run that did not exit cleanly, clear it and retry once
		clearProfileLocks(dir)
		ctx, err = engine.LaunchPersistentContext(dir, opts)
	}
	if err != nil {
		_ = pw.Stop()
		return nil, err
	}
	ctx.SetDefaultTimeout(60000)

	var page playwright.Page
	if pages := ctx.Pages(); len(pages) > 0 {
		page = pages[0]
	} else if page, err = ctx.NewPage(); err != nil {
		_ = ctx.Close()
		_ = pw.Stop()
		return nil, err
	}
	return &Session{pw: pw, ctx: ctx, Page: page}, nil
}

// Cookies reads li_at / JSESSIONID straight out of the live browser context.
func (s *Session) Cookies() Creds {
	var c Creds
	cookies, err := s.ctx.Cookies("https://www.linkedin.com")
	if err != nil {
		return c
	}
	for _, ck := range cookies {
		switch ck.Name {
		case "li_at":
			c.LiAt = ck.Value
		case "JSESSIONID":
			c.JSession = strings.Trim(ck.Value, `"`)
		}
	}
	return c
}

// Grab is the whole flow: go to LinkedIn, and if already logged in return the
// cookies immediately; otherwise wait (up to timeout) for the user to sign in,
// polling the cookies until li_at appears. progress is called with human status.
func (s *Session) Grab(ctx context.Context, timeout time.Duration, progress func(string)) (Creds, error) {
	say := func(m string) {
		if progress != nil {
			progress(m)
		}
	}
	say("opening linkedin…")
	if _, err := s.Page.Goto(feedURL, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
	}); err != nil {
		_, _ = s.Page.Goto(loginURL, playwright.PageGotoOptions{WaitUntil: playwright.WaitUntilStateDomcontentloaded})
	}

	if c := s.Cookies(); c.OK() {
		say("already signed in · reading cookies")
		s.fillWho(&c)
		return c, nil
	}

	say("waiting for you to log in in the browser window…")
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return Creds{}, ctx.Err()
		default:
		}
		if c := s.Cookies(); c.OK() {
			say("captured cookies")
			s.fillWho(&c)
			return c, nil
		}
		s.Page.WaitForTimeout(1500)
	}
	return Creds{}, fmt.Errorf("timed out waiting for login")
}

// fillWho best-effort reads the display name and public handle from the page.
func (s *Session) fillWho(c *Creds) {
	if _, err := s.Page.Goto(feedURL, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
	}); err != nil {
		return
	}
	s.Page.WaitForTimeout(1200)
	if v, err := s.Page.Evaluate(`() => {
		const link = document.querySelector('a[href*="/in/"]');
		let pub = "";
		if (link) { const mm = link.href.match(/\/in\/([^\/?#]+)/); if (mm) pub = mm[1]; }
		const nameEl = document.querySelector('.profile-card-name, .t-16.t-black.t-bold, [aria-label^="Photo of"]');
		let name = nameEl ? (nameEl.textContent || nameEl.getAttribute('aria-label') || "").trim() : "";
		name = name.replace(/^Photo of /,'');
		return { name, pub };
	}`); err == nil {
		if m, ok := v.(map[string]any); ok {
			if n, _ := m["name"].(string); n != "" {
				c.Name = n
			}
			if p, _ := m["pub"].(string); p != "" {
				c.PublicID = p
			}
		}
	}
}

// DefaultBrowser prefers real Chrome, else Firefox.
func DefaultBrowser() string {
	if chromeExe() != "" {
		return "chrome"
	}
	return "firefox"
}

// Available lists which browsers we can actually launch (Chrome first).
func Available() []string {
	var out []string
	if chromeExe() != "" {
		out = append(out, "chrome")
	}
	out = append(out, "firefox")
	return out
}
