package browser

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/codeyevsky/liout/internal/model"

	"github.com/mxschmitt/playwright-go"
)

// SearchPeople runs a People search inside the logged-in browser and scrapes the
// results from the page · no Voyager API, so it can't 401 on a stale queryId.
func (s *Session) SearchPeople(ctx context.Context, keywords, geoURN string, max int, connections bool, progress func(string)) ([]model.Person, error) {
	if max <= 0 {
		max = 30
	}
	say := func(m string) {
		if progress != nil {
			progress(m)
		}
	}
	seen := map[string]bool{}
	var out []model.Person

	for page := 1; len(out) < max && page <= 40; page++ {
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		default:
		}
		u := searchURL(keywords, geoURN, page, connections)
		say(fmt.Sprintf("page %d · loading…", page))
		if _, err := s.Page.Goto(u, playwright.PageGotoOptions{WaitUntil: playwright.WaitUntilStateDomcontentloaded}); err != nil {
			if len(out) > 0 {
				return out, nil
			}
			return nil, err
		}
		// bounced to a login / checkpoint wall?
		if cur := s.Page.URL(); strings.Contains(cur, "/login") || strings.Contains(cur, "/checkpoint") || strings.Contains(cur, "/authwall") {
			return out, fmt.Errorf("LinkedIn asked to log in again · open Session and sign in")
		}
		// wait for the actual results list (not the nav bar) to render
		if _, err := s.Page.WaitForSelector(`main a[href*="/in/"]`, playwright.PageWaitForSelectorOptions{
			Timeout: playwright.Float(20000),
		}); err != nil {
			if page == 1 {
				return out, fmt.Errorf("no results appeared · try different keywords")
			}
			break
		}
		// let lazy rows fill in
		for i := 0; i < 6; i++ {
			s.Page.Mouse().Wheel(0, 1600)
			s.Page.WaitForTimeout(700)
		}
		s.Page.WaitForTimeout(800)
		raw, err := s.evalPeople()
		if err != nil {
			if len(out) > 0 {
				return out, nil
			}
			return nil, err
		}
		people := toPeople(raw)
		fresh := 0
		for _, p := range people {
			if p.ProfileID == "" || seen[p.ProfileID] {
				continue
			}
			seen[p.ProfileID] = true
			out = append(out, p)
			fresh++
			if len(out) >= max {
				break
			}
		}
		say(fmt.Sprintf("page %d · %d found (%d total)", page, fresh, len(out)))
		if fresh == 0 {
			break
		}
	}
	if len(out) > max {
		out = out[:max]
	}
	return out, nil
}

func searchURL(keywords, geoURN string, page int, connections bool) string {
	q := url.Values{}
	q.Set("keywords", keywords)
	q.Set("origin", "GLOBAL_SEARCH_HEADER")
	if page > 1 {
		q.Set("page", fmt.Sprintf("%d", page))
	}
	u := "https://www.linkedin.com/search/results/people/?" + q.Encode()
	if geoURN != "" {
		u += "&geoUrn=" + url.QueryEscape(`["`+geoURN+`"]`)
	}
	if connections {
		u += "&network=" + url.QueryEscape(`["F"]`) // F = 1st-degree (your connections)
	}
	return u
}

// evalPeople runs the collector, retrying once if a navigation destroys the context.
func (s *Session) evalPeople() (any, error) {
	raw, err := s.Page.Evaluate(collectPeopleJS)
	if err != nil && strings.Contains(err.Error(), "context was destroyed") {
		s.Page.WaitForTimeout(1500)
		raw, err = s.Page.Evaluate(collectPeopleJS)
	}
	return raw, err
}

const collectPeopleJS = `() => {
  const root = document.querySelector('main') || document;
  const out = [];
  const seen = {};
  const noise = /^(connect|follow|message|pending|following|1st|2nd|3rd|status is|view |linkedin member)/i;
  const drop = /(mutual connection|followers|·|•)/i;
  for (const a of root.querySelectorAll('a[href*="/in/"]')) {
    const m = a.getAttribute('href').match(/\/in\/([^\/?#]+)/);
    if (!m) continue;
    const id = decodeURIComponent(m[1]);
    if (seen[id]) continue;
    const sp = a.querySelector('span[aria-hidden="true"]');
    let name = sp ? (sp.innerText || '').trim() : (a.innerText || '').split('\n')[0].trim();
    if (!name || /^(view|status is|linkedin member)/i.test(name)) continue;
    // climb to the result card: a small block holding this one person
    let box = a;
    for (let i = 0; i < 10 && box; i++) {
      const p = box.parentElement;
      if (!p) break;
      box = p;
      const lines = (box.innerText || '').split('\n').map(x => x.trim()).filter(Boolean);
      const links = box.querySelectorAll('a[href*="/in/"]').length;
      if (lines.length >= 3 && links <= 3) break;
    }
    let headline = '', location = '', about = '';
    if (box) {
      const lines = (box.innerText || '').split('\n').map(s => s.trim()).filter(Boolean);
      const clean = lines.filter(l => l !== name && !noise.test(l) && !drop.test(l));
      headline = clean.find(l => l.length >= 8) || '';
      const rest = clean.slice(clean.indexOf(headline) + 1);
      location = rest.find(l => l.length >= 3 && l.length <= 44 && /[A-Za-zÀ-ÿ]/.test(l)) || '';
      // about: the longest remaining line (usually the "✦ … since …" summary)
      for (const l of clean) {
        const t = l.replace(/^[✦•\-\s]+/, '');
        if (t !== headline && t !== location && t.length > about.length && t.length > 20) about = t;
      }
    }
    seen[id] = 1;
    out.push({ id, name, headline, location, about, url: 'https://www.linkedin.com/in/' + id });
  }
  return out;
}`

func toPeople(raw any) []model.Person {
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	var out []model.Person
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id := strings.TrimSpace(str(m["id"]))
		if id == "" || strings.Contains(id, "search") {
			continue
		}
		p := model.Person{
			ProfileID:  id,
			FullName:   strings.TrimSpace(str(m["name"])),
			Headline:   strings.TrimSpace(str(m["headline"])),
			Location:   strings.TrimSpace(str(m["location"])),
			ProfileURL: strings.TrimSpace(str(m["url"])),
		}
		p.Set("about", strings.TrimSpace(str(m["about"])))
		out = append(out, p)
	}
	return out
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
