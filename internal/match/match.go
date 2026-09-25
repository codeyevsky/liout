// Package match filters people with regex rules and pulls title/company out of a headline.
package match

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/codeyevsky/liout/internal/model"
	"github.com/codeyevsky/liout/internal/textx"
)

type Rule struct {
	Field   string `json:"field"`   // full_name, headline, title, company, location, any, or any CSV column
	Pattern string `json:"pattern"` // Go regexp
	re      *regexp.Regexp
}

// Rules: all = every rule must match, any = at least one, none = none may match.
type Rules struct {
	All           []Rule `json:"all"`
	Any           []Rule `json:"any"`
	None          []Rule `json:"none"`
	CaseSensitive bool   `json:"case_sensitive"`
}

func Load(path string) (*Rules, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r Rules
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := r.Compile(); err != nil {
		return nil, err
	}
	return &r, nil
}

func (r *Rules) Compile() error {
	sets := [][]Rule{r.All, r.Any, r.None}
	for _, set := range sets {
		for i := range set {
			pat := set[i].Pattern
			if !r.CaseSensitive && !strings.HasPrefix(pat, "(?") {
				pat = "(?i)" + pat
			}
			re, err := regexp.Compile(pat)
			if err != nil {
				return fmt.Errorf("regex %q: %w", set[i].Pattern, err)
			}
			set[i].re = re
			if set[i].Field == "" {
				set[i].Field = "any"
			}
		}
	}
	return nil
}

func (r *Rules) Match(p model.Person) bool {
	if r == nil {
		return true
	}
	for _, ru := range r.All {
		if !ru.re.MatchString(p.Field(ru.Field)) {
			return false
		}
	}
	for _, ru := range r.None {
		if ru.re.MatchString(p.Field(ru.Field)) {
			return false
		}
	}
	if len(r.Any) > 0 {
		for _, ru := range r.Any {
			if ru.re.MatchString(p.Field(ru.Field)) {
				return true
			}
		}
		return false
	}
	return true
}

// Extractor splits a headline with named regex groups and cleans up the company name.
type Extractor struct {
	headline *regexp.Regexp
	strip    *regexp.Regexp
}

const (
	DefaultHeadlinePattern = `(?i)^\s*(?P<title>[^|@]+?)\s+(?:at|@|nezdinde|-)\s+(?P<company>[^|·•]+)`
	// Note: a separator is required before the suffix, otherwise "Bursa" would become "Bur".
	DefaultCompanyStrip = `(?i)[\s,\.\-]+(a\.?ş\.?|ltd(\.|\s)*(şti\.?)?|limited|şti\.?|inc\.?|llc\.?|l\.l\.c\.?|gmbh|corp(oration)?\.?|co\.|b\.v\.?|s\.a\.?|s\.r\.l\.?|plc|holding|group|grup|teknoloji|teknolojileri|yazılım|bilişim|danışmanlık|ticaret|sanayi|ve)\s*$`
)

func NewExtractor(headlinePattern, companyStrip string) (*Extractor, error) {
	if headlinePattern == "" {
		headlinePattern = DefaultHeadlinePattern
	}
	if companyStrip == "" {
		companyStrip = DefaultCompanyStrip
	}
	h, err := regexp.Compile(headlinePattern)
	if err != nil {
		return nil, fmt.Errorf("headline_pattern: %w", err)
	}
	s, err := regexp.Compile(companyStrip)
	if err != nil {
		return nil, fmt.Errorf("company_strip: %w", err)
	}
	return &Extractor{headline: h, strip: s}, nil
}

// Apply fills empty title/company fields from the headline and normalizes the name.
func (e *Extractor) Apply(p *model.Person) {
	p.FullName = textx.CleanName(p.FullName)
	if p.FirstName == "" {
		p.FirstName = textx.FirstToken(p.FullName)
	}
	if p.LastName == "" {
		p.LastName = textx.LastToken(p.FullName)
	}
	if (p.Title == "" || p.Company == "") && p.Headline != "" {
		m := e.headline.FindStringSubmatch(p.Headline)
		if m != nil {
			for i, name := range e.headline.SubexpNames() {
				val := strings.TrimSpace(m[i])
				switch name {
				case "title":
					if p.Title == "" {
						p.Title = val
					}
				case "company":
					if p.Company == "" {
						p.Company = val
					}
				}
			}
		}
	}
	if p.ProfileURL == "" && p.ProfileID != "" {
		p.ProfileURL = "https://www.linkedin.com/in/" + p.ProfileID
	}
	p.Company = e.CleanCompany(p.Company)
	p.Title = strings.TrimSpace(textx.StripSymbols(p.Title))
	p.Title = strings.Join(strings.Fields(p.Title), " ")
}

// CleanCompany turns "Trendyol Teknoloji A.Ş. 🚀" into "Trendyol".
func (e *Extractor) CleanCompany(c string) string {
	c = strings.TrimSpace(textx.StripSymbols(c))
	c = strings.Join(strings.Fields(c), " ")
	for i := 0; i < 3; i++ {
		n := strings.TrimSpace(e.strip.ReplaceAllString(c, ""))
		if n == c || n == "" {
			break
		}
		c = n
	}
	return strings.Trim(c, " ,.-|")
}
