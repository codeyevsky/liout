package browser

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/mxschmitt/playwright-go"
)

// SendResult says what actually happened for one person.
type SendResult struct {
	Action string // "message" | "invite" | "skip"
	Note   string
}

// SendTo opens a person's profile and either messages them (if possible) or
// sends a connection request with the text as a note · all through the real UI,
// so it never touches the Voyager API. preferInvite forces the connect path.
func (s *Session) SendTo(ctx context.Context, profileID, text string, preferInvite, skipExisting bool) (SendResult, error) {
	url := "https://www.linkedin.com/in/" + profileID + "/"
	if _, err := s.Page.Goto(url, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(30000),
	}); err != nil {
		return SendResult{}, fmt.Errorf("could not open profile")
	}
	if cur := s.Page.URL(); strings.Contains(cur, "/login") || strings.Contains(cur, "/authwall") {
		return SendResult{}, fmt.Errorf("LinkedIn asked to log in; open Session and sign in again")
	}
	s.Page.WaitForSelector(`main`, playwright.PageWaitForSelectorOptions{Timeout: playwright.Float(15000)})
	s.Page.WaitForTimeout(500)

	if !preferInvite {
		done, already, err := s.messageViaCompose(text, skipExisting)
		if err != nil {
			return SendResult{}, err
		}
		if already {
			return SendResult{Action: "already"}, nil
		}
		if done {
			return SendResult{Action: "message"}, nil
		}
	}
	// fall back to a connection request with a note (max 300 chars)
	note := text
	if r := []rune(note); len(r) > 300 {
		note = strings.TrimSpace(string(r[:297])) + "…"
	}
	if ok, err := s.tryConnect(note); err != nil {
		return SendResult{}, err
	} else if ok {
		return SendResult{Action: "invite", Note: note}, nil
	}
	return SendResult{Action: "skip"}, fmt.Errorf("could not message or connect")
}

// messageViaCompose pulls the recipient URN from the profile page and opens the
// messaging composer directly (messaging/thread/new/?recipient=...), bypassing the
// flaky profile "Message" button. Returns (sent, err).
func (s *Session) messageViaCompose(text string, skipExisting bool) (done, already bool, err error) {
	html, e := s.Page.Content()
	if e != nil {
		return false, false, nil
	}
	id := extractURN(html)
	if id == "" {
		return false, false, nil // no URN -> let caller try Connect
	}
	composeURL := "https://www.linkedin.com/messaging/thread/new/?recipient=" + id
	if _, e := s.Page.Goto(composeURL, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(30000),
	}); e != nil {
		return false, false, nil
	}
	if _, e := s.Page.WaitForSelector(
		`.msg-form__contenteditable, div[contenteditable="true"][role="textbox"]`,
		playwright.PageWaitForSelectorOptions{Timeout: playwright.Float(12000)},
	); e != nil {
		return false, false, nil
	}
	s.Page.WaitForTimeout(500)

	// already talked to them (even years ago, before liout)? skip, unless disabled.
	if skipExisting {
		if v, e := s.Page.Evaluate(`() => document.querySelectorAll('.msg-s-event-listitem, .msg-s-message-list__event, li.msg-s-event-listitem').length`); e == nil {
			if cnt, ok := v.(int); ok && cnt > 0 {
				return false, true, nil
			}
			if f, ok := v.(float64); ok && f > 0 {
				return false, true, nil
			}
		}
	}

	box := s.firstVisible(
		`.msg-form__contenteditable div[contenteditable="true"]`,
		`.msg-form__contenteditable`,
		`div[contenteditable="true"][role="textbox"]`,
	)
	if box == nil {
		return false, false, nil
	}
	_ = box.Click()
	if e := box.PressSequentially(text, playwright.LocatorPressSequentiallyOptions{Delay: playwright.Float(6)}); e != nil {
		_ = box.Fill(text)
	}
	s.Page.WaitForTimeout(300)
	send := s.firstVisible(
		`button.msg-form__send-button`,
		`.msg-form button[type="submit"]`,
		`.msg-form__send-btn`,
		`.msg-form button:has-text("Send")`,
	)
	if send == nil {
		return false, false, nil
	}
	if e := send.Click(playwright.LocatorClickOptions{Timeout: playwright.Float(8000)}); e != nil {
		return false, false, nil
	}
	s.Page.WaitForTimeout(900)
	return true, false, nil
}

// tryMessage clicks Message// tryMessage clicks Message// tryMessage clicks Message, types, and sends. Returns (sent, err).
func (s *Session) tryMessage(text string) (bool, error) {
	if !s.clickAction("message") {
		return false, nil
	}
	// wait for the compose overlay to open
	s.Page.WaitForSelector(
		`.msg-form__contenteditable, div[aria-label^="Write a message"], div[contenteditable="true"][role="textbox"]`,
		playwright.PageWaitForSelectorOptions{Timeout: playwright.Float(12000)},
	)
	s.Page.WaitForTimeout(800)
	box := s.firstVisible(
		`.msg-form__contenteditable div[contenteditable="true"]`,
		`.msg-form__contenteditable`,
		`div[aria-label^="Write a message"]`,
		`div[contenteditable="true"][role="textbox"]`,
	)
	if box == nil {
		return false, nil // no compose box (maybe InMail-gated) -> let caller try Connect
	}
	if err := box.Click(); err != nil {
		return false, err
	}
	if err := box.PressSequentially(text, playwright.LocatorPressSequentiallyOptions{Delay: playwright.Float(8)}); err != nil {
		_ = box.Fill(text)
	}
	s.Page.WaitForTimeout(500)

	send := s.firstVisible(
		`button.msg-form__send-button`,
		`.msg-form button[type="submit"]`,
		`.msg-form__send-btn`,
		`.msg-overlay-conversation-bubble button:has-text("Send")`,
		`.msg-form button:has-text("Send")`,
	)
	if send == nil {
		return false, nil // couldn't send via message -> try Connect
	}
	if err := send.Click(playwright.LocatorClickOptions{Timeout: playwright.Float(8000)}); err != nil {
		return false, err
	}
	s.Page.WaitForTimeout(1500)
	// close any open chat overlay so it doesn't cover the next profile
	s.Page.Evaluate(`() => {
		for (const b of document.querySelectorAll('button[aria-label*="Close"], button[aria-label*="close"]')) {
			const l = (b.getAttribute('aria-label')||'');
			if (/conversation|messaging|chat/i.test(l)) { b.click(); }
		}
	}`)
	s.Page.WaitForTimeout(400)
	return true, nil
}

// tryConnect clicks Connect (directly or via More), adds the note, and sends.
func (s *Session) tryConnect(note string) (bool, error) {
	if !s.clickAction("connect") {
		return false, nil
	}
	s.Page.WaitForTimeout(1200)

	if add := s.firstVisible(`button[aria-label="Add a note"]`, `button:has-text("Add a note")`); add != nil {
		_ = add.Click()
		s.Page.WaitForTimeout(700)
		if ta := s.firstVisible(`textarea#custom-message`, `textarea[name="message"]`, `.connect-button-send-invite__custom-message`); ta != nil {
			_ = ta.Fill(note)
		}
	}
	s.Page.WaitForTimeout(400)
	send := s.firstVisible(
		`button[aria-label="Send invitation"]`,
		`button[aria-label="Send now"]`,
		`.artdeco-modal button:has-text("Send")`,
		`button:has-text("Send")`,
	)
	if send == nil {
		return false, fmt.Errorf("connect send button not found")
	}
	if err := send.Click(playwright.LocatorClickOptions{Timeout: playwright.Float(8000)}); err != nil {
		return false, err
	}
	s.Page.WaitForTimeout(1200)
	return true, nil
}

// clickAction finds the profile's Message/Connect action button (never the nav)
// and clicks it in-page. Robust against LinkedIn's changing aria-labels.
func (s *Session) clickAction(which string) bool {
	js := `(which) => {
		// exclude the global nav and the right-hand "More profiles" sidebar / msg overlay,
		// so we only ever act on THIS profile's own action bar.
		const bad = (el) => el.closest('.global-nav, #global-nav, aside, .scaffold-layout__aside, .msg-overlay-container, .msg-overlay-list-bubble');
		const vis = (el) => { const r = el.getBoundingClientRect(); return r.width > 1 && r.height > 1; };
		const match = (el) => {
			const label = (el.getAttribute('aria-label') || '').trim();
			const text = (el.innerText || '').replace(/\s+/g, ' ').trim();
			if (which === 'message') return /^Message( |$)/.test(label) || text === 'Message';
			return /^(Invite|Connect)( |$)/.test(label) || text === 'Connect';
		};
		const cands = Array.from(document.querySelectorAll('button, a')).filter(el => !bad(el) && vis(el) && match(el));
		cands.sort((a, b) => {
			const ra = a.getBoundingClientRect(), rb = b.getBoundingClientRect();
			return (ra.top - rb.top) || (ra.left - rb.left);
		});
		if (cands.length) { cands[0].scrollIntoView({block: 'center'}); cands[0].click(); return true; }
		return false;
	}`
	for i := 0; i < 15; i++ { // poll up to ~12s for the button to render, then click
		v, err := s.Page.Evaluate(js, which)
		if err == nil {
			if ok, _ := v.(bool); ok {
				s.Page.WaitForTimeout(1000)
				return true
			}
		}
		s.Page.WaitForTimeout(800)
	}
	return false
}

// visibleButtons lists// visibleButtons lists a few visible button labels in main, for diagnostics.
func (s *Session) visibleButtons() string {
	v, err := s.Page.Evaluate(`() => {
		const out = [];
		const root = document.querySelector('main') || document;
		for (const b of root.querySelectorAll('button, a')) {
			if (out.length >= 6) break;
			const r = b.getBoundingClientRect();
			if (r.width < 1 || r.height < 1) continue;
			const t = (b.getAttribute('aria-label') || b.innerText || '').trim().replace(/\s+/g,' ').slice(0, 24);
			if (t) out.push(t);
		}
		return out.join(', ');
	}`)
	if err != nil {
		return "?"
	}
	if str, ok := v.(string); ok {
		return str
	}
	return "?"
}

// firstVisible returns the first selector that resolves to a visible element.
func (s *Session) firstVisible(selectors ...string) playwright.Locator {
	for _, sel := range selectors {
		all := s.Page.Locator(sel)
		n, err := all.Count()
		if err != nil {
			continue
		}
		for i := 0; i < n && i < 8; i++ {
			loc := all.Nth(i)
			if vis, err := loc.IsVisible(); err == nil && vis {
				return loc
			}
		}
	}
	return nil
}

var reURN = regexp.MustCompile(`ACoAA[A-Za-z0-9_-]{6,}`)

// extractURN finds the first fsd_profile id (ACoAA…) in the page HTML.
func extractURN(html string) string {
	return reURN.FindString(html)
}
