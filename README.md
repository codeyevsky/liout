# liout

**liout** — send a custom first message to the right people on LinkedIn, from one centered terminal UI. It drives a real browser (Chrome/Firefox) through Playwright, so it uses your own logged-in LinkedIn session — no password, no API token.

```
░█░░░▀█▀░█▀█░█░█░▀█▀░
░█░░░░█░░█░█░█░█░░█░░
░▀▀▀░▀▀▀░▀▀▀░▀▀▀░░▀░░
find the right people · send a custom first message
```

Search people (everyone or just your connections), keep a local library of message templates with easy placeholders, preview per person, then send — skipping anyone you've already talked to. Everything happens inside one keyboard-driven, centered TUI.

## Install

Requires Go 1.25+.

```bash
go install github.com/codeyevsky/liout/cmd/liout@latest
```

or build from a clone:

```bash
git clone https://github.com/codeyevsky/liout.git
cd liout
go build -o liout ./cmd/liout
./liout
```

First run creates `config.json` + `blacklist.txt` and drops you at sign-in. If the Playwright browser binaries aren't present yet, liout downloads them once automatically.

## The menu

```
SETUP
  s  Session       sign in with your browser (Chrome or Firefox)

PEOPLE
  f  Search        find people (keywords) — everyone or my connections
  l  View list     browse saved searches; open one; remove people

MESSAGE
  m  Messages      your saved message templates — add / edit / choose active
  p  Preview       render the active message for everyone, paged

SEND
  !  Send          message everyone on a saved search (visible browser)
  h  Sent          who you've already messaged (3×10 grid; remove entries)
```

`↑↓` / `j k` move, a number or letter jumps, `enter` runs, `?` help, `q` quits. Everything is centered and adapts to terminal width. `Ctrl+C` inside an action cancels just that action.

## How it works

### Sign in
**Session** opens a real browser at LinkedIn. If you're already logged in there, liout grabs the session instantly; otherwise you log in normally and it reads the cookies. It uses a persistent profile, so next time it's instant. Cookies are stored in `session.json` (mode `0600`). `LI_AT` / `LI_JSESSIONID` env vars also work.

### Find people
**Search** takes one or more keywords (added one at a time), a count, and a scope: **everyone** or **my connections** (1st-degree only). It runs the search in the browser and scrapes name, headline, location and a one-line summary — no fragile private API, so it can't break on an endpoint change. Each search is saved to history; **View list** shows them, lets you open one, and remove people you don't want. Optional `rules.json` regex filters are applied when present.

### Your messages
**Messages** manages `.tmpl` files in `messages/`. Write plain text with friendly placeholders — no template syntax to learn:

| Placeholder | Fills with |
|---|---|
| `[name]` | first name (cleaned, e.g. `Ayşe`) |
| `[company]` | their company (auto-falls back to "your company") |
| `[title]` | their role |
| `[headline]` | their LinkedIn headline |
| `[location]` | their city / country |
| `[about]` | one line about their current role, if found |
| `[spin Hi\|Hey\|Hello]` | picks one per person, so messages vary |
| `[company\|our team]` | your own fallback when a field is empty |

Anything inside a `/* ... */` block is a note and is **not** sent. Advanced `{{ ... }}` Go-template syntax (`def`, `spin`, `upper`, …) still works.

**Preview** renders the active message for everyone in a saved search, in a paged, scrollable view — nothing is sent.

### Send
**Send** picks a saved search and messages everyone on it, in a visible browser:

- Opens each profile, pulls the recipient URN, opens the messaging composer directly, types the rendered message and clicks Send. Falls back to a **connection request with a note** when direct messaging isn't available.
- **Skips people you've already talked to** (checks the real LinkedIn conversation — even messages from years ago, before liout). This is a per-run toggle.
- Never messages the same person twice (local ledger `data/sent.jsonl`), resumes if interrupted, and honours `blacklist.txt`.
- A small random delay between sends (`delay_min_sec`–`delay_max_sec`, default 3–8 s); no aggressive quotas.
- "my connections" searches always message (no connect-request prompt).

**Sent** shows everyone you've messaged in a 3-column × 10-row grid; remove an entry with `d` to let liout message them again.

## Files

```
cmd/liout/        CLI + TUI (menu, search, messages, send, sent)
internal/browser/ Playwright: opens a real browser, searches, sends
internal/linkedin/ cookie client (session verification)
internal/match/   regex rules, blacklist, headline → title/company
internal/tmpl/    message rendering + [placeholder] expansion
internal/textx/   Turkish-aware name cleanup, casing, case suffixes
internal/store/   sent ledger (dedupe, resume) + CSV I/O
internal/style/   256-colour palette + highlight helpers
```

Runtime files (git-ignored, created as you go): `config.json`, `session.json`, `blacklist.txt`, `messages/`, `data/` (searches + sent ledger).

```bash
go test ./internal/...
```

## Note

LinkedIn restricts automated access and bulk messaging in its Terms of Service; the account risk is yours. Defaults are deliberately conservative. Keep lists relevant and small, and don't message people the outreach isn't actually for.

Modeled on [githubFlex](https://github.com/codeyevsky/ghFlex).
