package linkedin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/codeyevsky/liout/internal/model"
)

// Enrich fills in title, company, location and connection degree from the profile page.
// It completes the partial data that search results return.
func (c *Client) Enrich(ctx context.Context, p *model.Person) error {
	if p.ProfileID == "" {
		return fmt.Errorf("no profile id")
	}
	paths := []string{
		"/voyager/api/identity/profiles/" + p.ProfileID + "/profileView",
		"/voyager/api/identity/dash/profiles?q=memberIdentity&memberIdentity=" + p.ProfileID,
	}
	var lastErr error
	for _, path := range paths {
		b, err := c.Do(ctx, http.MethodGet, path, nil)
		if err != nil {
			lastErr = err
			if strings.Contains(err.Error(), "session invalid") {
				return err
			}
			continue
		}
		if applyProfile(b, p) {
			if p.URN == "" {
				p.URN = ExtractURNID(b)
			}
			return nil
		}
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("could not read profile: %s", p.ProfileID)
}

func applyProfile(b []byte, p *model.Person) bool {
	var root any
	if err := json.Unmarshal(b, &root); err != nil {
		return false
	}
	found := false
	var bestTitle, bestCompany string

	walk(root, func(m map[string]any) {
		// the main profile node
		if fn, ok := m["firstName"].(string); ok {
			if ln, ok2 := m["lastName"].(string); ok2 {
				name := strings.TrimSpace(fn + " " + ln)
				if name != "" && len(name) > len(p.FullName) {
					p.FullName, p.FirstName, p.LastName = name, fn, ln
					found = true
				}
			}
		}
		if h := txt(m["headline"]); h != "" && p.Headline == "" {
			p.Headline = h
			found = true
		}
		if l := firstNonEmpty(txt(m["locationName"]), txt(m["geoLocationName"]), txt(m["defaultLocalizedName"])); l != "" && p.Location == "" {
			p.Location = l
			found = true
		}
		if d := str(m["distance"]); d != "" {
			p.Set("connection", shortDistance(d))
		}
		if dm, ok := m["distance"].(map[string]any); ok {
			if v := str(dm["value"]); v != "" {
				p.Set("connection", shortDistance(v))
			}
		}
		// a position node carries both company and title
		comp := firstNonEmpty(txt(m["companyName"]), txt(m["company"]))
		ttl := txt(m["title"])
		if comp != "" && ttl != "" && bestCompany == "" {
			// prefer the entry marked current
			if cur, ok := m["current"].(bool); !ok || cur {
				bestCompany, bestTitle = comp, ttl
			}
		}
	})
	if bestCompany != "" {
		if p.Company == "" {
			p.Company = bestCompany
		}
		if p.Title == "" {
			p.Title = bestTitle
		}
		found = true
	}
	return found
}

func shortDistance(d string) string {
	switch {
	case strings.HasSuffix(d, "_1"):
		return "1."
	case strings.HasSuffix(d, "_2"):
		return "2."
	case strings.HasSuffix(d, "_3"):
		return "3."
	case strings.Contains(d, "SELF"):
		return "me"
	case strings.Contains(d, "OUT_OF_NETWORK"):
		return "out of network"
	}
	return d
}

// RepliedProfiles scans the inbox and returns the ids of people who messaged us.
// Used to skip people who already replied when sending follow-ups.
func (c *Client) RepliedProfiles(ctx context.Context, count int) (map[string]bool, error) {
	if count <= 0 {
		count = 50
	}
	me, err := c.Me(ctx)
	if err != nil {
		return nil, err
	}
	paths := []string{
		fmt.Sprintf("/voyager/api/messaging/conversations?keyVersion=LEGACY_INBOX&count=%d", count),
		fmt.Sprintf("/voyager/api/messaging/conversations?count=%d", count),
	}
	out := map[string]bool{}
	var lastErr error
	for _, path := range paths {
		b, err := c.Do(ctx, http.MethodGet, path, nil)
		if err != nil {
			lastErr = err
			continue
		}
		collectSenders(b, me, out)
		if len(out) > 0 {
			return out, nil
		}
	}
	if len(out) == 0 && lastErr != nil {
		return nil, lastErr
	}
	return out, nil
}

// collectSenders harvests profile ids from "from"/"sender" fields in the JSON.
func collectSenders(b []byte, me string, out map[string]bool) {
	var root any
	if err := json.Unmarshal(b, &root); err != nil {
		return
	}
	walk(root, func(m map[string]any) {
		for _, key := range []string{"from", "*from", "sender", "senderUrn", "*sender"} {
			v, ok := m[key]
			if !ok {
				continue
			}
			var raw string
			switch t := v.(type) {
			case string:
				raw = t
			case map[string]any:
				raw = str(t["entityUrn"]) + " " + str(t["*miniProfile"]) + " " + str(t["hostIdentityUrn"])
				if mp, ok := t["miniProfile"].(map[string]any); ok {
					raw += " " + str(mp["entityUrn"])
				}
			}
			if id := ExtractURNID([]byte(raw)); id != "" && id != me {
				out[id] = true
			}
		}
	})
}

// ProfileURL builds a profile address from a public identifier.
func ProfileURL(publicID string) string { return BaseURL + "/in/" + publicID }
