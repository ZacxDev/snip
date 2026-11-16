package shared

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultTimeout bounds the fetch. 🔴 MEASURED CHOICE, NOT A ROUND NUMBER
// COPIED FROM THE SIBLING: the picker's own asr-style default is 60s, which on
// a hotkey path is worse than failing — the operator pressed a key and is
// watching an empty list. 2s is longer than a mesh round trip to a homelab
// service and short enough that a dead endpoint reads as dead while his hand
// is still on the keyboard.
const DefaultTimeout = 2 * time.Second

// DefaultLimit is the newest-N bound the picker asks for. The store has no
// expiry by design, so `limit` is the ONLY growth bound that matters: without
// one, a picker open eventually drags the whole history over the mesh.
const DefaultLimit = 100

// Clip is one row of the shared store, exactly the JSON the service serves:
// {id, body, device, created_at, bump_count}. created_at is epoch
// MILLISECONDS (not seconds — a seconds reading renders every clip as 56 years
// old), and bump_count is how many times this body was re-captured, which is
// the dedup made visible.
type Clip struct {
	ID        int64  `json:"id"`
	Body      string `json:"body"`
	Device    string `json:"device"`
	CreatedAt int64  `json:"created_at"`
	BumpCount int    `json:"bump_count"`
}

// listResponse is the envelope the list route answers with. The rows are under
// `clips`; a bare array is NOT what the service serves.
type listResponse struct {
	Clips []Clip `json:"clips"`
}

// errResponse is the service's failure shape: {error, detail}. The detail is a
// written-out sentence and it is passed through VERBATIM — the endpoint is the
// only thing that knows why it refused, and replacing its sentence with "400
// Bad Request" throws that away.
type errResponse struct {
	Error  string `json:"error"`
	Detail string `json:"detail"`
}

// Client reads the shared store over plain stdlib HTTP.
//
// 🔴 IT NEVER RETURNS AN EMPTY LIST FOR A FAILURE. Every transport, status and
// parse failure comes back as an error with a reason in words; only a 200 that
// genuinely carried no rows is (nil, nil)-shaped. An empty result and an
// unreachable store are different states and the picker renders them
// differently (three renders, not two).
type Client struct {
	cfg  Config
	http *http.Client
}

// New builds a client for a validated config.
func New(cfg Config) *Client {
	return &Client{cfg: cfg, http: &http.Client{Timeout: DefaultTimeout}}
}

// NewWithClient is the test seam: an httptest server plus its own
// *http.Client. 🔴 NO TEST IN THIS REPO OPENS THE LIVE ENDPOINT — the config
// file is never read by a test and no fixture carries a real URL or token.
func NewWithClient(cfg Config, hc *http.Client) *Client {
	return &Client{cfg: cfg, http: hc}
}

// Rows returns the newest `limit` clips, newest first — the server's order,
// verbatim.
//
// 🔴 NO REORDERING IS APPLIED HERE OR ANYWHERE DOWNSTREAM. Chronological
// newest-first IS the requirement; the sibling catalog picker applies a
// most-recently-used order on browse, and doing that to a chronological store
// would reorder the one property it is for.
func (c *Client) Rows(limit int) ([]Clip, error) {
	if limit <= 0 {
		limit = DefaultLimit
	}
	u := strings.TrimRight(c.cfg.URL, "/") + "/v1/clips?limit=" + strconv.Itoa(limit)
	body, err := c.get(u)
	if err != nil {
		return nil, err
	}
	var resp listResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("unreadable answer from the clip store: %v", err)
	}
	return resp.Clips, nil
}

// There is deliberately NO Search method here. The server owns `?q=`, but snip
// never asks it to filter: the picker matches LOCALLY over the rows it already
// holds (the body travels with the paste, so the picker has every clip it
// displays) — design §4.4.
//
// 🔴 AND THE TWO MATCHERS WOULD NOT AGREE, which is why a server-side filter is
// not a drop-in alternative to the local one. The server's index is FTS5
// trigram: it matches genuine substrings ACROSS word boundaries, and because a
// query whose shortest token is under 3 RUNES is served by a LIKE scan instead
// of the index, its case-folding depends on the query's LENGTH (the index folds
// case beyond ASCII; the scan folds ASCII only). RankShared is a per-token
// substring test over the body and knows none of that. If a client ever does
// need the server's filter, it is a new method with that difference in its
// contract — not a second matcher nothing branches on.

// Delete removes one clip from the store, by the id the list route served.
//
// 🔴 A 404 IS SUCCESS, NOT A FAILURE. The operator asked for the row to be
// gone, and a store answering "no such id" has already granted exactly that —
// the clip was deleted from the phone, or by a second press of this key
// against a list fetched before that happened. Reporting it as an error would
// show a failure for the outcome that was asked for, and would leave a row on
// screen that no longer exists anywhere. This is the service's own contract
// (DELETE /v1/clips/{id} → 204, and 404 for an id that is already gone), so it
// is read here rather than discovered in the picker.
//
// 🔴 EVERY OTHER NON-2xx IS AN ERROR WITH ITS REASON, and the caller must NOT
// remove the row on one — a dropped row after a refused delete would show the
// operator a deletion that did not happen.
func (c *Client) Delete(id int64) error {
	u := strings.TrimRight(c.cfg.URL, "/") + "/v1/clips/" + strconv.FormatInt(id, 10)
	body, code, err := c.do(http.MethodDelete, u)
	if err != nil {
		return err
	}
	switch code {
	case http.StatusNoContent, http.StatusOK, http.StatusNotFound:
		return nil
	}
	return statusError(code, body)
}

// get performs one authenticated GET and returns the body of a 200, mapping
// every failure to a sentence.
func (c *Client) get(u string) ([]byte, error) {
	body, code, err := c.do(http.MethodGet, u)
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, statusError(code, body)
	}
	return body, nil
}

// do performs one authenticated request and hands back the body AND the status
// code, so each route decides for itself which codes are success — the list
// route wants 200 only; delete also accepts 204 and 404. A transport or
// request-construction failure comes back as an error; a status does not.
//
// 🔴 ONE PLACE BUILDS THE AUTHENTICATED REQUEST. Open-coding the header per
// method is how one of them ends up unauthenticated, or how one of them starts
// printing the credential.
//
// 🔴 THE TOKEN IS NEVER IN A MESSAGE. The URL is, because the operator needs
// to know which endpoint failed; the Authorization header is not, and the
// request is never dumped.
func (c *Client) do(method, u string) ([]byte, int, error) {
	req, err := http.NewRequest(method, u, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("bad clip store URL %q: %v", c.cfg.URL, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		// A transport failure's own text names the host and the cause
		// (connection refused, i/o timeout); it is the message.
		return nil, 0, fmt.Errorf("clip store unreachable: %v", transportMessage(err))
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if readErr != nil && resp.StatusCode == http.StatusOK {
		return nil, resp.StatusCode, fmt.Errorf("clip store: reading the answer: %v", readErr)
	}
	return body, resp.StatusCode, nil
}

// transportMessage drops the stdlib's `<Method> "<url>": ` wrapper, which
// repeats a URL the caller already names, while keeping the cause.
//
// 🔴 IT UNWRAPS THE TYPE, NOT THE SPELLING. A prefix test for `Get ` was
// correct while GET was the only method; DELETE renders `Delete "<url>": …`
// and would have gone unstripped. *url.Error is what net/http actually
// returns, and its Err is the cause for every method.
func transportMessage(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return strings.TrimSpace(ue.Err.Error())
	}
	return err.Error()
}

// statusError turns a non-200 into a sentence. The service's own `detail` is
// used when it sent one — it is a written-out explanation of the refusal and
// nothing this client could compose is better. 401 is spelled out, because
// "unauthorized" on this path means one specific thing: the configured token
// is wrong.
func statusError(code int, body []byte) error {
	var e errResponse
	if json.Unmarshal(body, &e) == nil && e.Detail != "" {
		return fmt.Errorf("clip store refused (%d): %s", code, e.Detail)
	}
	if code == http.StatusUnauthorized {
		return fmt.Errorf("clip store refused (401): %s is not accepted", KeyToken)
	}
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		return fmt.Errorf("clip store refused (%d): %s", code, e.Error)
	}
	return fmt.Errorf("clip store refused (%d)", code)
}

// Age renders a clip's age the way the sibling history row does: a short
// single-unit string. Pure and clock-injected (now is passed, never read) so a
// test pins it without freezing time.
func Age(createdAtMS, nowMS int64) string {
	d := nowMS - createdAtMS
	if d < 0 {
		// A clip stamped in the future is a clock skew between two devices,
		// not an error worth a branch in the UI: it reads as brand new.
		d = 0
	}
	switch s := d / 1000; {
	case s < 60:
		return strconv.FormatInt(s, 10) + "s"
	case s < 3600:
		return strconv.FormatInt(s/60, 10) + "m"
	case s < 86400:
		return strconv.FormatInt(s/3600, 10) + "h"
	default:
		return strconv.FormatInt(s/86400, 10) + "d"
	}
}
