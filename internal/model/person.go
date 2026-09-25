package model

import (
	"sort"
	"strings"
)

// Person is both the scraper's output and the template's data source.
type Person struct {
	ProfileID     string // publicIdentifier, e.g. "ayse-yilmaz-123"
	URN           string // urn:li:fsd_profile:ACoAA… (required to send a message)
	FullName      string
	FirstName     string
	LastName      string
	FormattedName string // when set in the CSV it overrides everything
	Headline      string
	Title         string
	Company       string
	Location      string
	ProfileURL    string
	Extra         map[string]string // every unrecognized CSV column
}

var CSVHeader = []string{
	"profile_id", "urn", "full_name", "first_name", "last_name",
	"formatted_name", "headline", "title", "company", "location", "profile_url",
}

func (p Person) Field(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "profile_id", "id":
		return p.ProfileID
	case "urn":
		return p.URN
	case "full_name", "name":
		return p.FullName
	case "first_name", "first":
		return p.FirstName
	case "last_name", "last":
		return p.LastName
	case "formatted_name":
		return p.FormattedName
	case "headline":
		return p.Headline
	case "title", "position":
		return p.Title
	case "company":
		return p.Company
	case "location":
		return p.Location
	case "profile_url", "url":
		return p.ProfileURL
	case "any", "*":
		return strings.Join([]string{p.FullName, p.Headline, p.Title, p.Company, p.Location}, " | ")
	}
	if p.Extra != nil {
		if v, ok := p.Extra[strings.ToLower(name)]; ok {
			return v
		}
	}
	return ""
}

func (p Person) Row(extraCols []string) []string {
	row := []string{
		p.ProfileID, p.URN, p.FullName, p.FirstName, p.LastName,
		p.FormattedName, p.Headline, p.Title, p.Company, p.Location, p.ProfileURL,
	}
	for _, c := range extraCols {
		row = append(row, p.Extra[c])
	}
	return row
}

func (p Person) ExtraCols() []string {
	cols := make([]string, 0, len(p.Extra))
	for k := range p.Extra {
		cols = append(cols, k)
	}
	sort.Strings(cols)
	return cols
}

func (p *Person) Set(field, value string) {
	switch strings.ToLower(strings.TrimSpace(field)) {
	case "profile_id", "id":
		p.ProfileID = value
	case "urn":
		p.URN = value
	case "full_name", "name":
		p.FullName = value
	case "first_name", "first":
		p.FirstName = value
	case "last_name", "last":
		p.LastName = value
	case "formatted_name":
		p.FormattedName = value
	case "headline":
		p.Headline = value
	case "title", "position":
		p.Title = value
	case "company":
		p.Company = value
	case "location":
		p.Location = value
	case "profile_url", "url":
		p.ProfileURL = value
	default:
		if p.Extra == nil {
			p.Extra = map[string]string{}
		}
		p.Extra[strings.ToLower(strings.TrimSpace(field))] = value
	}
}

func (p Person) Key() string {
	if p.ProfileID != "" {
		return p.ProfileID
	}
	return p.URN
}
