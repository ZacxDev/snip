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
- **`ctrl+g` shared clipboard** — an opt-in second keypress that lists a
  cross-device clip store as a *disjoint mode* with its own list, filter and
  keys, and pastes a clip body into the origin window. Inside it, **`ctrl+x`
  deletes the highlighted clip from the store**, behind a `y/N` confirm — on
  every device, with no undo and no trash. Unconfigured by default; see
  [The shared clipboard](#the-shared-clipboard-ctrlg).
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
  --ephemeral        paste F's contents WITHOUT loading the catalog
                     (the shared clipboard's path)
snip list            the catalog as a table (--category C to filter)
snip search QUERY    ranked matches (the same matcher the picker runs)
snip get LABEL       the raw body ({{clip}} unresolved)
snip expand LABEL    the resolved expansion ({{clip}} from the X clipboard)
```

Exit codes: `0` success/no-op (a picker cancel is 0), `1` a real failure
(config, X11, paste path), `2` usage.

Picker keys: `↑/↓` move · `⏎` paste · `ctrl+e` edit · `ctrl+g` shared
clipboard · `esc` cancel.

Shared-clipboard keys (`ctrl+g`, a disjoint table — nothing falls through to
the ones above): `↑/↓` move · `⏎` paste the clip · `ctrl+x` delete the clip
from the store, behind a `y/N` confirm · `esc` back to the catalog.

## The shared clipboard (`ctrl+g`)

A second keypress inside the picker switches to a **cross-device clipboard**:
a separate, chronological, unlabelled store served over your own network, so
text captured on another device can be pasted into the window you were just
in. It is **opt-in and unconfigured by default** — with no config file the
mode says so and the catalog picker is unaffected.

It is a **mode, not a merged list**, and deliberately so:

- a clip has no label and no declared `search_terms`, and its searchable text
  is the *body* — which the catalog matcher never reads. Two populations, two
  match functions; the catalog's `Rank` is untouched.
- the key table is **disjoint**: nothing falls through to the browse keys, so
  `ctrl+e` (edit-and-land, which has no meaning for a row from another
  device) is swallowed rather than acting on a catalog row.
- the list is the server's order, **verbatim** — chronological newest-first,
  with no recency re-sort. Typing filters it by substring over the body.
- the fetch is **lazy**: it happens on the keypress, never at picker open, so
  an open that never asks for a clip pays no network cost.
- a failed fetch is **not** an empty list. The three states are rendered
  distinctly — *fetching…*, *the store is empty*, and *unreachable, with the
  reason in words* — because "you have no clips" and "the store is down" are
  different facts.
- pasting carries the **body**, not an id, so the detached helper never
  re-looks-up a row that a delete from another device may have removed in the
  meantime. `{{clip}}` is **not** expanded in a clip body (it came from
  another machine and has no claim on this one's clipboard), and a clip paste
  is not recorded in the catalog's browse order.
- clip fire rows carry their own telemetry `source` (`snipclip`), so an audit
  of *catalog* demand never counts a clipboard paste as demand for a snippet.
  Those rows also carry **no query**: the shared filter matches on the clip
  *body*, so what you type here is a fragment of the captured text, and it is
  dropped before the row is written (the `method` flag — whether anything was
  typed at all — is kept). A catalog row still carries its query, which is
  label words.

### Configuring it

Write `~/.config/snip/shared.env`, mode **0600** — an env file with two keys:

```sh
SNIP_SHARED_URL=http://your-store.example:8080
SNIP_SHARED_TOKEN=your-bearer-token
```

**snip ships no default endpoint and no default token, on purpose.** This is
a public repository; an address compiled in here would be the *author's*
network, and a token distributed in a build artifact (a Nix store path is
world-readable) would be readable by every local user on the machine. A
launcher wrapper may override the *path* (`SNIP_SHARED_ENV`); never the value.

Missing, unreadable or short credentials are a **named failure in the picker**,
never a silent empty list — the file path and both key names are in the
message. The URL must be `http(s)`; the token must be at least 8 characters.

The service it talks to is four routes — `GET /v1/clips?limit=N&q=…`,
`POST /v1/clips`, `DELETE /v1/clips/{id}`, `GET /healthz`. snip **reads**
(`GET /v1/clips?limit=N`) and filters locally, and **deletes**
(`DELETE /v1/clips/{id}`) when the operator presses `ctrl+x` and answers the
confirm. It does **not** capture — nothing in snip writes a clip — and it does
not use the server's `q=` search, whose semantics are its own (a full-text
index's substring matching is not the same rule as this client's per-token body
substring test, and the two must not be assumed to agree).

### Deleting a clip (`ctrl+x`)

`ctrl+x` in the shared mode removes the highlighted clip **from the store**:
every device loses it, there is no trash and the service has no undo. So:

- it only **asks**. The chord raises a modal `[y/N]` naming the clip's id and a
  preview of its body, and nothing is called until `y`. The prompt is rendered
  from the armed state itself, so no other event can take the question off the
  screen while leaving the answer live.
- the list on screen must be the store's **current answer**. While a fetch or a
  delete is in flight — re-entering the mode, or the re-list after a deletion —
  `⏎` and `ctrl+x` are inert, because the rows are from the previous read.
- the list is **re-read from the store** after a delete lands rather than edited
  locally, so a refused delete can never show a deletion that did not happen.
- a 404 is **success**: the row is gone either way, which is what was asked for.

## Nix

The repo is a flake — the package, overlay and app are all canonical here:

```sh
nix build github:ZacxDev/snip#snip      # or: nix run github:ZacxDev/snip
# as an input, with the consumer's nixpkgs:
#   inputs.snip.url = "github:ZacxDev/snip";
#   inputs.snip.inputs.nixpkgs.follows = "nixpkgs";
```

`package.nix` reads the version out of `cmd/snip/version.go` (never a hand-held
literal), pins the runtime tools the binary execs (`xclip`, `xdotool`,
`dunstify`, `bash`, `nvim`) by store path, and runs **no** Go checks at build
time — the tests run in CI; a red test must never be able to fail a machine
upgrade. It does not pin `i3` (the binary falls back to `xdotool` when
`i3-msg` is absent) and does not ship a landing script.

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
| `SNIP_SHARED_ENV` | shared-clipboard config **path** override (default `~/.config/snip/shared.env`, mode 0600, holding `SNIP_SHARED_URL` + `SNIP_SHARED_TOKEN`). The path only — never the token value |

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

## Cutting a release

`cmd/snip/version.go` is the single source of truth. `package.nix` reads that
one line with a regex and **throws at flake-eval time** if it does not match
exactly once, so a reformatted declaration breaks every consumer's evaluation
instead of quietly shipping a wrong version; `TestVersionDeclarationShape`
pins the same shape in CI, so you find out one step earlier.

1. **Check whether `main` has already moved past the last tag.** Nothing
   enforces this, which is why it is the step that gets skipped:

   ```sh
   git log --oneline "$(git describe --tags --abbrev=0)"..main
   ```

   Non-empty output while `buildVersion` still holds the tagged value means
   merged work is sitting inside an already-published release number.

2. Bump `buildVersion` in `cmd/snip/version.go` — that line only, and do not
   reformat it.
3. Commit and push to `main`. Pushing the bump installs nothing; see step 5.
4. Tag it to match, and push the tag:

   ```sh
   git tag v0.2.0 && git push origin v0.2.0
   ```

5. **Update the consumer, or you have shipped nothing.** devrc pins snip as a
   flake input, so merging and tagging change nothing on any machine until
   someone runs `nix flake update snip` there and switches. A release that
   stops at step 4 leaves the feature merged, tagged, and still absent from
   the installed binary.

### Why steps 1 and 5 are written down

They are the two halves of one measured failure. `v0.1.1` was cut on
2026-11-01. #2 — the whole `ctrl+g` shared clipboard — merged on 2026-11-16
with `buildVersion` left at `"0.1.1"`, while devrc's `flake.lock` still
pinned the commit *before* it, so the installed binary carried no
shared-clipboard code at all. Because the version had not moved, the build
**with** the feature and the build **without** it were both named
`snip-0.1.1`, differing only by store hash: `snip --version` could not
separate them, and neither could a derivation name in a diff of two system
closures. #3 bumped it to 0.2.0 on 2026-11-17.

So an unbumped version is not a cosmetic lapse — it removes the cheapest
signal anyone has that two builds differ, which is precisely the signal you
reach for when a merged feature appears to be missing. This is a checklist
rather than a CI gate on purpose: the natural gate (fail while commits sit
between the last tag and `HEAD` with the version still equal to the tag's) is
red on every feature PR until someone bumps, and a permanently-red check only
teaches everyone to click through it.

## Provenance

snip began inside the author's configuration repository
([innovation-upstream/devrc](https://github.com/innovation-upstream/devrc)) —
as the espanso replacement for a nix-managed desktop — and was extracted here
in 2026 with a fresh history. That repo still carries the nix packaging, the
landing script and the desktop integration.

## License

[MIT](LICENSE)
