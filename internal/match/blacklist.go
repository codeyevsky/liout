package match

import (
	"bufio"
	"os"
	"regexp"
	"strings"

	"github.com/codeyevsky/liout/internal/model"
)

// Blacklist holds people who must never be messaged. One rule per line:
//
//	ayse-yilmaz-123          -> profile id
//	@Trendyol                -> company name contains this
//	re:headline:(recruiter)  -> regex on a field
//	# comment line
type Blacklist struct {
	ids       map[string]bool
	companies []string
	rules     []Rule
}

func LoadBlacklist(path string) (*Blacklist, error) {
	b := &Blacklist{ids: map[string]bool{}}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return b, nil // no blacklist file means an empty blacklist
		}
		return nil, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		switch {
		case strings.HasPrefix(line, "re:"):
			rest := strings.TrimPrefix(line, "re:")
			field, pat := "any", rest
			if i := strings.Index(rest, ":"); i > 0 {
				field, pat = rest[:i], rest[i+1:]
			}
			re, err := regexp.Compile("(?i)" + pat)
			if err != nil {
				return nil, err
			}
			b.rules = append(b.rules, Rule{Field: field, Pattern: pat, re: re})
		case strings.HasPrefix(line, "@"):
			b.companies = append(b.companies, strings.ToLower(strings.TrimPrefix(line, "@")))
		default:
			if i := strings.Index(line, "/in/"); i >= 0 {
				line = strings.Trim(line[i+4:], "/")
			}
			b.ids[strings.ToLower(line)] = true
		}
	}
	return b, sc.Err()
}

// Blocked reports whether this person is blacklisted, and by which rule.
func (b *Blacklist) Blocked(p model.Person) (bool, string) {
	if b == nil {
		return false, ""
	}
	if b.ids[strings.ToLower(p.ProfileID)] {
		return true, "id blacklisted"
	}
	comp := strings.ToLower(p.Company)
	for _, c := range b.companies {
		if c != "" && strings.Contains(comp, c) {
			return true, "company blacklisted: " + c
		}
	}
	for _, r := range b.rules {
		if r.re.MatchString(p.Field(r.Field)) {
			return true, "rule: " + r.Field + " ~ " + r.Pattern
		}
	}
	return false, ""
}

func (b *Blacklist) Size() int {
	if b == nil {
		return 0
	}
	return len(b.ids) + len(b.companies) + len(b.rules)
}

// AppendBlacklist adds one line to the blacklist file.
func AppendBlacklist(path, line string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(strings.TrimSpace(line) + "\n")
	return err
}
