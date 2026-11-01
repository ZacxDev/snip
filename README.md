# snip

Hotkey → fuzzy snippet picker → paste. One static Go binary, **no daemon** —
every invocation is a one-shot process and the catalog is read at process
start, so a config change is picked up by the next fire.

Built with [Bubble Tea](https://github.com/charmbracelet/bubbletea). Inspired
by espanso; replaces its daemon + YAML with a picker TUI over a plain JSON
catalog.

## Features

- **Picker TUI** — opens in *browse* order (most recently used first, from a
  use-recency file); typing switches to the matcher's ranking. Rows are
  label-only: the label is the row's identity, the matcher ranks declared
  `search_terms` above label prose, and every whitespace-separated token of
  the query must match (multi-word queries work).
- **Detached paste** — the picker spawns a helper that waits for the picker
  window to die, refocuses the origin window, sends the paste key, restores
  the clipboard, and records a fire row. If the origin window is gone, it
  aborts rather than paste into "whatever is focused now".
- **ctrl+e edit** — edits the selected row's body in `$SNIP_EDITOR` (or
  `$VISUAL`/`$EDITOR`, default `nvim`), then lands the change through a
  landing script (see [The ctrl+e landing](#the-ctrle-landing)). An emptied
  body offers to **delete** the row after an explicit `[y/N]` confirm — the
  mode is a flag, never an inference from an empty file.
- **`{{clip}}`** — the one variable a body may carry, resolved at expand time
  from the X clipboard (no shell vars, no date vars, no forms).
- **Fire telemetry** — every open/cancel/select appends a JSONL row to a
  local spool, so an audit can tell used rows from dead ones.

## Install

```sh
go install github.com/ZacxDev/snip/cmd/snip@latest
```

or from a checkout:

```sh
git clone https://github.com/ZacxDev/snip
cd snip
go build -o snip ./cmd/snip
```

Requires Go 1.26+. Runtime prerequisites (Linux/X11): `xclip`, `xdotool`;
optionally `i3` (`i3-msg` is used to refocus when available, `xdotool`
otherwise) and `dunstify` for failure toasts.

## The catalog

`snip` reads a plain JSON array — `$SNIP_CONFIG`, else
`$XDG_CONFIG_HOME/snip/snippets.json`, else `~/.config/snip/snippets.json`:

```json
[
  {
    "label": "End-of-session ritual",
    "body": "reflect on work done and key learnings",
    "search_terms": ["end", "session", "ritual"],
    "category": "prompt"
  }
]
```

- `label` — the row's identity. Must be non-empty and unique; the loader
  refuses the whole file otherwise, loudly, naming the file and row.
- `body` — the expansion text; may carry `{{clip}}`.
- `search_terms` — declared tokens; a query reaching a row through them
  outranks one that only spells it in the label.
- `category` — picks the row icon and label colour (path/ssh/kubeconfig/
  prompt/clipboard are known; anything else renders the plain default).

Typically the file is *generated* from a declarative source (nix, home-manager,
a template) at deploy time — that is what the ctrl+e landing assumes.

## Commands

```
snip                 the picker TUI (default; also `snip picker`)
snip paste           the detached paste helper (spawned by the picker)
  --label L          the snippet to paste, by its label (required)
  --body-file F      paste F's contents instead of the artifact's body
snip list            the catalog as a table (--category C to filter)
snip search QUERY    ranked matches (the same matcher the picker runs)
snip get LABEL       the raw body ({{clip}} unresolved)
snip expand LABEL    the resolved expansion ({{clip}} from the X clipboard)
```

Exit codes: `0` success/no-op (a picker cancel is 0), `1` a real failure
(config, X11, paste path), `2` usage.

Picker keys: `↑/↓` move · `⏎` paste · `ctrl+e` edit · `esc` cancel.

## The ctrl+e landing

Editing lands through an external script: `$SNIP_REPO/scripts/snip-edit.sh`,
receiving the label (and `--delete` for the delete mode). The script's stderr
is passed through verbatim into the TUI, so its refusals are the message.

`SNIP_REPO` unset means **no landing**: ctrl+e reports that the edit landing
is unavailable, and nothing is changed. snip deliberately ships no default
checkout path and no landing script — where your catalog's source lives is
your setup's business. If your catalog is a plain JSON file you manage by
hand, a landing script can be as small as "write the new body back into the
JSON array"; the contract is only: refuse loudly in one line when the landing
is not safe.

The delete mode is the landing script's explicit `--delete` flag — an emptied
body never deletes by inference.

## Environment

| Variable | Meaning |
| --- | --- |
| `SNIP_CONFIG` | catalog path override |
| `SNIP_RECENT` | use-recency file override (browse order) |
| `SNIP_EDITOR` / `VISUAL` / `EDITOR` | the ctrl+e editor (default `nvim`) |
| `SNIP_REPO` | checkout holding `scripts/snip-edit.sh` (unset = no landing) |
| `SNIP_RESTORE_DELAY_MS` | clipboard-restore delay (default 300) |
| `SNIP_ORIGIN_WINDOW` / `SNIP_ORIGIN_CLASS` | set by the WM keybind wrapper |
| `SNIP_QUERY` / `SNIP_OPEN_LATENCY_MS` | injected by the picker into the helper |
| `ACTIVITY_SPOOL_DIR` | fire-row spool override |

State paths: the recency file is `$XDG_STATE_HOME/snip/recent.json` (else
`~/.local/state/snip/recent.json`); fire rows go to
`$XDG_STATE_HOME/activity/spool` (else `~/.local/state/activity/spool`).

## Wire it to a hotkey

Bind your WM key to the picker and let it capture the origin window:

```sh
snip-launch ... snip picker        # any wrapper that exports SNIP_ORIGIN_WINDOW/CLASS
```

A wrapper that records the focused window's id and class into
`SNIP_ORIGIN_WINDOW` / `SNIP_ORIGIN_CLASS` before exec'ing `snip picker` is
all snip needs to paste back into the window you fired from.

## Development

```sh
go vet ./...
go test ./...
```

Release builds stamp the version via `-X main.buildVersion=vX.Y.Z`; the
declaration's shape is pinned by `TestVersionDeclarationShape`.

## Provenance

snip began inside the author's configuration repository
([innovation-upstream/devrc](https://github.com/innovation-upstream/devrc)) —
as the espanso replacement for a nix-managed desktop — and was extracted here
in 2026 with a fresh history. That repo still carries the nix packaging, the
landing script and the desktop integration.

## License

[MIT](LICENSE)
