package linkedin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/codeyevsky/liout/internal/model"
)

// DefaultSearchURL targets the graphql endpoint LinkedIn currently uses.
// The queryId hash changes from time to time; when it does, use the paste-a-URL
// flow instead (SearchByURL / "Search by pasted URL" in the menu).
const DefaultSearchURL = "/voyager/api/graphql" +
	"?variables=(start:{start},origin:GLOBAL_SEARCH_HEADER," +
	"query:(keywords:{keywords},flagshipSearchIntent:SEARCH_SRP," +
	"queryParameters:List((key:resultType,value:List(PEOPLE)){filters}),includeFiltersInResponse:false))" +
	"&queryId=voyagerSearchDashClusters.b0928897b71bd00a5a7291755dcd64f0"

// LegacySearchURL is the older REST endpoint, still open on some accounts.
const LegacySearchURL = "/voyager/api/search/dash/clusters" +
	"?decorationId=com.linkedin.sdui.requests.search.SearchClusterCollection-176" +
	"&origin=GLOBAL_SEARCH_HEADER&q=all" +
	"&query=(keywords:{keywords},flagshipSearchIntent:SEARCH_SRP," +
	"queryParameters:List((key:resultType,value:List(PEOPLE)){filters}),includeFiltersInResponse:false)" +
	"&start={start}&count={count}"

type SearchOpts struct {
	Keywords  string
	URLTmpl   string
	Filters   string // raw Voyager filter, e.g. ",(key:geoUrn,value:List(102105699))"
	Count     int
	MaxResult int
	Debug     bool
}

// SearchPeople runs a paginated people search, extracting each page generically.
func (c *Client) SearchPeople(ctx context.Context, o SearchOpts, onPage func(int, []model.Person)) ([]model.Person, error) {
	tmpl := o.URLTmpl
	if tmpl == "" {
		tmpl = DefaultSearchURL
	}
	if o.Count <= 0 {
		o.Count = 10
	}
	if o.MaxResult <= 0 {
		o.MaxResult = 100
	}
	seen := map[string]bool{}
	var out []model.Person

	for start := 0; len(out) < o.MaxResult; start += o.Count {
		u := strings.NewReplacer(
			"{keywords}", url.QueryEscape(o.Keywords),
			"{filters}", o.Filters,
			"{start}", strconv.Itoa(start),
			"{count}", strconv.Itoa(o.Count),
		).Replace(tmpl)

		b, err := c.Do(ctx, http.MethodGet, u, nil)
		if err != nil {
			if len(out) > 0 {
				return out, nil // keep what we already have
			}
			return nil, err
		}
		page := ExtractPeople(b)
		fresh := page[:0:0]
		for _, p := range page {
			if p.ProfileID == "" || seen[p.ProfileID] {
				continue
			}
			seen[p.ProfileID] = true
			fresh = append(fresh, p)
		}
		if onPage != nil {
			onPage(start, fresh)
		}
		if len(fresh) == 0 {
			break
		}
		out = append(out, fresh...)
	}
	if len(out) > o.MaxResult {
		out = out[:o.MaxResult]
	}
	return out, nil
}

var (
	reStartVar = regexp.MustCompile(`start:\d+`)
	reStartQry = regexp.MustCompile(`([?&]start=)\d+`)
)

// SearchByURL paginates a ready-made search request copied from the browser's
// Network tab. It keeps working even when LinkedIn changes its endpoint or queryId.
func (c *Client) SearchByURL(ctx context.Context, raw string, maxResult, count int, onPage func(int, []model.Person)) ([]model.Person, error) {
	raw = strings.TrimSpace(strings.Trim(strings.TrimSpace(raw), `"'`))
	if raw == "" {
		return nil, fmt.Errorf("empty URL")
	}
	if i := strings.Index(raw, "linkedin.com"); i >= 0 {
		raw = BaseURL + raw[i+len("linkedin.com"):]
	}
	if count <= 0 {
		count = 10
	}
	if maxResult <= 0 {
		maxResult = 100
	}
	pageable := reStartVar.MatchString(raw) || reStartQry.MatchString(raw)
	seen := map[string]bool{}
	var out []model.Person

	for start := 0; len(out) < maxResult; start += count {
		u := raw
		if reStartVar.MatchString(u) {
			u = reStartVar.ReplaceAllString(u, "start:"+strconv.Itoa(start))
		}
		if reStartQry.MatchString(u) {
			u = reStartQry.ReplaceAllString(u, "${1}"+strconv.Itoa(start))
		}
		b, err := c.Do(ctx, http.MethodGet, u, nil)
		if err != nil {
			if len(out) > 0 {
				return out, nil
			}
			return nil, err
		}
		fresh := filterNew(ExtractPeople(b), seen)
		if onPage != nil {
			onPage(start, fresh)
		}
		out = append(out, fresh...)
		if len(fresh) == 0 || !pageable {
			break
		}
	}
	if len(out) > maxResult {
		out = out[:maxResult]
	}
	return out, nil
}

func filterNew(page []model.Person, seen map[string]bool) []model.Person {
	var fresh []model.Person
	for _, p := range page {
		if p.ProfileID == "" || seen[p.ProfileID] {
			continue
		}
		seen[p.ProfileID] = true
		fresh = append(fresh, p)
	}
	return fresh
}

var (
	rePublicID = regexp.MustCompile(`linkedin\.com/in/([A-Za-z0-9À-ÿ%_\-]+)`)
	reInPath   = regexp.MustCompile(`^/in/([A-Za-z0-9À-ÿ%_\-]+)`)
	reRawPID   = regexp.MustCompile(`"publicIdentifier"\s*:\s*"([A-Za-z0-9À-ÿ%_\-]+)"`)
)

// ExtractPeople walks the JSON tree instead of relying on the Voyager schema, turning
// every node that carries a profile id into a person.
func ExtractPeople(b []byte) []model.Person {
	var root any
	if err := json.Unmarshal(b, &root); err != nil {
		return rawFallback(b)
	}
	acc := map[string]*model.Person{}
	walk(root, func(m map[string]any) {
		p, ok := nodeToPerson(m)
		if !ok {
			return
		}
		if cur, exists := acc[p.ProfileID]; exists {
			merge(cur, p)
			return
		}
		cp := p
		acc[p.ProfileID] = &cp
	})
	if len(acc) == 0 {
		return rawFallback(b)
	}
	out := make([]model.Person, 0, len(acc))
	for _, p := range acc {
		out = append(out, *p)
	}
	return out
}

func walk(v any, fn func(map[string]any)) {
	switch t := v.(type) {
	case map[string]any:
		fn(t)
		for _, child := range t {
			walk(child, fn)
		}
	case []any:
		for _, child := range t {
			walk(child, fn)
		}
	}
}

func nodeToPerson(m map[string]any) (model.Person, bool) {
	var p model.Person

	if s, ok := m["publicIdentifier"].(string); ok {
		p.ProfileID = s
	}
	nav := str(m["navigationUrl"])
	if nav == "" {
		nav = str(m["url"])
	}
	if p.ProfileID == "" && nav != "" {
		if mm := rePublicID.FindStringSubmatch(nav); mm != nil {
			p.ProfileID = mm[1]
		} else if mm := reInPath.FindStringSubmatch(nav); mm != nil {
			p.ProfileID = mm[1]
		}
	}
	if p.ProfileID == "" || strings.Contains(p.ProfileID, "company") {
		return p, false
	}
	p.ProfileID = strings.TrimSuffix(p.ProfileID, "/")

	if urn := ExtractURNID([]byte(str(m["entityUrn"]) + " " + str(m["trackingUrn"]) + " " + str(m["objectUrn"]))); urn != "" {
		p.URN = urn
	}
	p.FullName = firstNonEmpty(txt(m["title"]), joinName(m), txt(m["name"]), txt(m["fullName"]))
	p.Headline = firstNonEmpty(txt(m["primarySubtitle"]), txt(m["headline"]), txt(m["occupation"]), txt(m["subtitle"]))
	p.Location = firstNonEmpty(txt(m["secondarySubtitle"]), txt(m["locationName"]), txt(m["geoRegion"]))
	p.ProfileURL = BaseURL + "/in/" + p.ProfileID

	if p.FullName == "" && p.Headline == "" && p.URN == "" {
		return p, false
	}
	return p, true
}

func merge(dst *model.Person, src model.Person) {
	if dst.URN == "" {
		dst.URN = src.URN
	}
	if dst.FullName == "" {
		dst.FullName = src.FullName
	}
	if dst.Headline == "" {
		dst.Headline = src.Headline
	}
	if dst.Location == "" {
		dst.Location = src.Location
	}
}

// rawFallback collects ids with a plain regex when the JSON is unparseable or unrecognized.
func rawFallback(b []byte) []model.Person {
	seen := map[string]bool{}
	var out []model.Person
	add := func(id string) {
		id = strings.TrimSuffix(id, "/")
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, model.Person{ProfileID: id, ProfileURL: BaseURL + "/in/" + id})
	}
	for _, m := range reRawPID.FindAllSubmatch(b, -1) {
		add(string(m[1]))
	}
	for _, m := range rePublicID.FindAllSubmatch(b, -1) {
		add(string(m[1]))
	}
	return out
}

func txt(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case map[string]any:
		if s, ok := t["text"].(string); ok {
			return strings.TrimSpace(s)
		}
		if inner, ok := t["text"]; ok {
			return txt(inner)
		}
		if inner, ok := t["title"]; ok {
			return txt(inner)
		}
	}
	return ""
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func joinName(m map[string]any) string {
	f, l := txt(m["firstName"]), txt(m["lastName"])
	return strings.TrimSpace(f + " " + l)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// ExtractFromBytes pulls people out of a manually saved Voyager response.
func ExtractFromBytes(b []byte) ([]model.Person, error) {
	people := ExtractPeople(b)
	if len(people) == 0 {
		return nil, fmt.Errorf("no people found in file")
	}
	return people, nil
}
