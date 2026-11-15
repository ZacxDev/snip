// Package shared is snip's client for the cross-device clipboard store: the
// config that names the endpoint, and the stdlib HTTP client that reads it.
//
// 🔴 THE CONFIG FILE IS THE OPERATOR'S, NOT THIS TOOL'S. This repo is PUBLIC
// (MIT), so the endpoint and the token are CONFIGURATION and nothing else:
// there is no default URL compiled in, no fixture carrying a real one, and no
// test that opens the live path. A default here would be the AUTHOR's mesh,
// exactly as effects.snipRepoDir refuses to guess the author's checkout.
//
// 🔴 MISSING OR SHORT CREDENTIALS ARE A NAMED FAILURE, NEVER A SILENT ONE —
// and never a guessed default. The picker's shared view then reports why it is
// unavailable instead of rendering an empty list, because an empty list is
// indistinguishable from a working feature with nothing in it.
//
// 🔴 NOT VIA NIX EITHER. /nix/store is world-readable, so a token exported
// from a generated launcher wrapper would be readable by every local user. The
// launcher may carry a PATH override ($SNIP_SHARED_ENV); never the value.
package shared

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// envFileName is the operator-written file, mode 0600, modelled on
// ~/.config/stt/env: KEY=value lines, `#` comments, nothing else.
const envFileName = "shared.env"

// The two keys. They are spelled HERE and nowhere else in any repo —
// referenced from devrc's SECRETS.md as prose — so there is nothing to drift.
const (
	KeyURL   = "SNIP_SHARED_URL"
	KeyToken = "SNIP_SHARED_TOKEN"
)

// MinTokenLen is the floor a configured token must clear. A one-character
// value is a truncated paste, not a credential, and failing it here is how the
// operator learns that instead of reading a 401 out of the picker.
const MinTokenLen = 8

// Config is the resolved endpoint. Zero value is never usable: Load returns an
// error instead, and the caller reports it.
type Config struct {
	URL   string
	Token string
}

// EnvPath resolves the config file: $SNIP_SHARED_ENV overrides (the launcher's
// path override, and what every test points at), else
// $XDG_CONFIG_HOME/snip/shared.env, else ~/.config/snip/shared.env — the same
// XDG resolution artifact.Path uses, so the two config files sit together.
func EnvPath() (string, error) {
	if p := os.Getenv("SNIP_SHARED_ENV"); p != "" {
		return p, nil
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "snip", envFileName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("snip: resolve home for %s: %w", envFileName, err)
	}
	return filepath.Join(home, ".config", "snip", envFileName), nil
}

// Load reads and validates the config. Every failure names the file and the
// remedy: the operator is standing at a picker that just told him the shared
// view is unavailable, and the reason has to be actionable.
func Load() (Config, error) {
	path, err := EnvPath()
	if err != nil {
		return Config{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, fmt.Errorf("no %s — write %s and %s into it (mode 0600)",
				path, KeyURL, KeyToken)
		}
		return Config{}, fmt.Errorf("snip: %s: %w", path, err)
	}
	defer f.Close()

	vals := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// `export KEY=value` is accepted: the file looks like a shell env file
		// and an operator who writes `export` should not get a mystery.
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		vals[strings.TrimSpace(k)] = unquote(strings.TrimSpace(v))
	}
	if err := sc.Err(); err != nil {
		return Config{}, fmt.Errorf("snip: %s: %w", path, err)
	}
	c := Config{URL: vals[KeyURL], Token: vals[KeyToken]}
	if err := c.Validate(path); err != nil {
		return Config{}, err
	}
	return c, nil
}

// unquote strips one layer of matching single or double quotes, because an env
// file written by hand usually has them and a token with literal quote
// characters in it would otherwise fail auth with no explanation.
func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// Validate refuses an empty URL, a URL that is not http(s), and a token under
// MinTokenLen. The file name is carried into every message: there is exactly
// one file to fix and the operator should not have to guess which.
func (c Config) Validate(path string) error {
	if c.URL == "" {
		return fmt.Errorf("%s: %s is empty", path, KeyURL)
	}
	if !strings.HasPrefix(c.URL, "http://") && !strings.HasPrefix(c.URL, "https://") {
		return fmt.Errorf("%s: %s must be an http(s) URL", path, KeyURL)
	}
	if len(c.Token) < MinTokenLen {
		return fmt.Errorf("%s: %s is shorter than %d characters", path, KeyToken, MinTokenLen)
	}
	return nil
}
