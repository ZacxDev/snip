// Command snip: hotkey → fuzzy-picker TUI → paste. One static binary, NO
// daemon — every subcommand is a one-shot process, and config is read at
// process start, every invocation, so a config change is picked up by the
// next fire.
//
// Commands:
//
//	snip             the picker TUI (the default; also `snip picker`)
//	snip paste       the DETACHED paste helper (spawned by the picker;
//	                 --label required; not run by hand)
//	snip list        the catalog as a table (--category to filter)
//	snip search Q    ranked matches — the SAME matcher the picker filters with
//	snip get L       the raw body of one label ({{clip}} unresolved)
//	snip expand L    the resolved expansion ({{clip}} from the X clipboard)
//
// Exit codes: 0 success/no-op (a picker CANCEL is exit 0 — a normal outcome,
// recorded as outcome=cancel); 1 a real failure (config, X11, paste path) —
// accompanied by a dunst toast from the GUI paths, NEVER a silent fail;
// 2 usage.
package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ZacxDev/snip/internal/artifact"
	"github.com/ZacxDev/snip/internal/effects"
	"github.com/ZacxDev/snip/internal/paste"
	"github.com/ZacxDev/snip/internal/picker"
	"github.com/ZacxDev/snip/internal/recent"
)

func main() {
	args := os.Args[1:]
	// 🔴 --version IS ANSWERED BEFORE ARGUMENT VALIDATION (mention-review's
	// contract): the value is the one default.nix read out of version.go and
	// stamped back via -X, so the store path and the compiled-in string are
	// provably one value.
	if len(args) == 1 && (args[0] == "--version" || args[0] == "-v") {
		fmt.Println(buildVersion)
		return
	}
	// No arguments IS the picker — the i3 keybind execs `snip picker`, but a
	// bare `snip` must not be a usage error that prints help and exits 0
	// (the clawgatectl lesson: a CLI that answers help with exit 0 for a
	// subcommand it does not have).
	if len(args) == 0 {
		args = []string{"picker"}
	}
	switch args[0] {
	case "picker":
		os.Exit(runPicker())
	case "paste":
		os.Exit(runPaste(args[1:]))
	case "list":
		os.Exit(runList(args[1:]))
	case "search":
		os.Exit(runSearch(args[1:]))
	case "get":
		os.Exit(runGet(args[1:]))
	case "expand":
		os.Exit(runExpand(args[1:]))
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "snip: unknown command %q\n", args[0])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `snip — hotkey → fuzzy picker → paste

  snip                 the picker TUI (default; also `+"`snip picker`"+`)
                       opens in BROWSE order: most recently used first
      ctrl+e           edit the selected row's body, then land the change
                       through the landing script (see SNIP_REPO)
  snip paste           the detached paste helper (spawned by the picker)
      --label L        the snippet to paste, by its label (required)
      --body-file F    paste F's contents instead of the artifact's body
                       (the picker's edit path; F is consumed and removed)
  snip list            the catalog as a table (--category C to filter)
  snip search QUERY    ranked matches (the same matcher the picker runs)
  snip get LABEL       the raw body ({{clip}} unresolved)
  snip expand LABEL    the resolved expansion ({{clip}} from the X clipboard)

Environment:
  SNIP_ORIGIN_WINDOW / SNIP_ORIGIN_CLASS   captured by the WM keybind wrapper
  SNIP_QUERY / SNIP_OPEN_LATENCY_MS        injected by the picker
  SNIP_RESTORE_DELAY_MS                    clipboard restore delay (default 300)
  SNIP_CONFIG                              artifact path override (tests)
  SNIP_RECENT                              the use-recency file the browse
                                           order comes from (tests)
  SNIP_REPO                                the checkout holding the ctrl+e
                                           landing script (scripts/snip-edit.sh);
                                           UNSET means the edit/delete landing
                                           is unavailable and ctrl+e reports why
  SNIP_EDITOR / VISUAL / EDITOR            the ctrl+e editor (default nvim)
`)
}

// loadOrDie loads the artifact for the CLI read paths. Failures are loud and
// name the file (the loader's contract).
func loadOrDie() []artifact.Snippet {
	path, err := artifact.Path()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	snippets, err := artifact.Load(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	return snippets
}

// orderedForBrowse is the catalog in BROWSE order: most recently USED first,
// then the never-used rows in artifact order (internal/recent).
//
// 🔴 IT IS APPLIED HERE, ONCE, AND ONLY TO THE TWO PATHS THAT SHOW A BROWSE.
// The picker opens on this order, and `snip search ”` prints it, which is what
// keeps "what the CLI shows is what the picker lists" true. `list`, `get` and
// `expand` are NOT reordered: `list` is the catalog as the nix file defines it,
// and reordering a thing whose label you are about to type would be a surprise.
//
// A failure to resolve the state path is not an error — the browse order simply
// degrades to artifact order, which is what the picker did before this existed.
func orderedForBrowse(snippets []artifact.Snippet) []artifact.Snippet {
	path, err := recent.Path()
	if err != nil {
		return snippets
	}
	return recent.Order(snippets, recent.Load(path))
}

// runPicker opens the picker TUI. Open latency is measured from process entry
// to the first render — the honest "hotkey → picker input-ready" bound minus
// only the alacritty spawn itself, which the latency budget accounts
// separately (scope doc §3).
func runPicker() int {
	start := time.Now()
	snippets := orderedForBrowse(loadOrDie())
	m := picker.New(snippets, effects.Live{})
	// Warm the first render synchronously: what it costs is what the
	// operator waits for before the rows are on screen and typable.
	_ = m.View()
	return picker.Run(m, time.Since(start).Milliseconds())
}

// runPaste is the detached helper: one label, one paste attempt, one
// telemetry row. Exit 0 on select; exit 1 on abort (the toast + row already
// said why).
func runPaste(args []string) int {
	label := ""
	bodyFile := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--label":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "snip paste: --label needs a value")
				return 2
			}
			i++
			label = args[i]
		case "--body-file":
			// 🔴 THE EDIT PATH'S ONLY WAY TO PASTE FRESH TEXT. The artifact is
			// written by home-manager and only regenerated by a ship, so after
			// a ctrl+e edit the deployed artifact still holds the OLD body.
			// The picker hands over the operator's edited text here instead.
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "snip paste: --body-file needs a value")
				return 2
			}
			i++
			bodyFile = args[i]
		default:
			// A positional label is accepted too — the helper is also the
			// human repair path ("snip paste 'End-of-session ritual'" into the
			// focused window).
			if label == "" && !strings.HasPrefix(args[i], "-") {
				label = args[i]
			}
		}
	}
	if label == "" {
		fmt.Fprintln(os.Stderr, "snip paste: --label is required")
		return 2
	}
	snippets := loadOrDie()
	if bodyFile != "" {
		// The file is the picker's scratch: consumed here, removed here, so a
		// successful edit leaves nothing behind in /tmp.
		defer os.Remove(bodyFile)
		body, err := os.ReadFile(bodyFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "snip paste: --body-file: %v\n", err)
			return 1
		}
		var ok bool
		snippets, ok = overrideBody(snippets, label, body)
		if !ok {
			fmt.Fprintf(os.Stderr, "snip paste: --body-file for an unknown label %q\n", label)
			return 1
		}
	}
	req := paste.RequestFromEnv(label)
	res := paste.Run(effects.Live{}, snippets, req)
	if res.Err != nil {
		// The abort row + toast already fired inside Run; the exit code is
		// for callers that check (the e2e harness does).
		fmt.Fprintf(os.Stderr, "snip paste: %v\n", res.Err)
		return 1
	}
	// 🔴 REMEMBER THE USE, for the picker's browse order. This runs AFTER the
	// paste has landed and its error is DISCARDED on purpose: the paste
	// succeeded, and failing it now over a ranking hint would be the
	// bookkeeping outranking the thing it exists to rank.
	if path, err := recent.Path(); err == nil {
		_ = recent.Touch(path, label, time.Now())
	}
	return 0
}

// overrideBody returns the catalog with `label`'s body replaced by `raw`, and
// whether that label was found.
//
// It is the ONE piece of the `--body-file` path worth separating: the label is
// the row's identity, and an override that silently did nothing (wrong label)
// would paste the OLD body while the operator believes he pasted the new one —
// so the not-found case is returned rather than swallowed, and the caller
// refuses the paste.
func overrideBody(snippets []artifact.Snippet, label string, raw []byte) ([]artifact.Snippet, bool) {
	out := make([]artifact.Snippet, len(snippets))
	copy(out, snippets)
	for i := range out {
		if out[i].Label == label {
			out[i].Body = string(raw)
			return out, true
		}
	}
	return snippets, false
}

// runList prints the catalog as a table.
func runList(args []string) int {
	category := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--category" && i+1 < len(args) {
			i++
			category = args[i]
			continue
		}
		fmt.Fprintf(os.Stderr, "snip list: unknown argument %q\n", args[i])
		return 2
	}
	for _, s := range loadOrDie() {
		if category != "" && s.Category != category {
			continue
		}
		terms := strings.Join(s.SearchTerms, " ")
		fmt.Printf("%s\t%s\t%s\n", s.Label, s.Category, terms)
	}
	return 0
}

// runSearch prints the ranked matches for a query — the SAME matcher and rank
// the picker filters with (picker.Rank is the one implementation), so what
// the CLI shows is what the picker lists. For an empty query that means the
// browse order too (orderedForBrowse), not the artifact order.
func runSearch(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "snip search: exactly one QUERY argument is required")
		return 2
	}
	for _, s := range picker.Rank(orderedForBrowse(loadOrDie()), args[0]) {
		fmt.Printf("%s\n", s.Label)
	}
	return 0
}

// runGet prints the raw body of one label.
func runGet(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "snip get: exactly one LABEL argument is required")
		return 2
	}
	s, ok := artifact.Find(loadOrDie(), args[0])
	if !ok {
		fmt.Fprintf(os.Stderr, "snip get: no label %q\n", args[0])
		return 1
	}
	fmt.Print(s.Body)
	return 0
}

// runExpand prints the resolved expansion: {{clip}} from the X clipboard.
func runExpand(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "snip expand: exactly one LABEL argument is required")
		return 2
	}
	s, ok := artifact.Find(loadOrDie(), args[0])
	if !ok {
		fmt.Fprintf(os.Stderr, "snip expand: no label %q\n", args[0])
		return 1
	}
	body := s.Body
	if strings.Contains(body, paste.ClipVar) {
		clip, _ := effects.Live{}.ClipboardGet()
		body = strings.ReplaceAll(body, paste.ClipVar, string(clip))
	}
	fmt.Print(body)
	return 0
}
