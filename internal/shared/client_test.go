package shared

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 🔴 INVARIANT GUARDS, AND A LOCAL STUB SERVER ONLY. Every case below runs
// against httptest; nothing here knows the live endpoint's address and no
// fixture carries a token that would work anywhere.
//
// The expected strings are LITERAL — never read back out of the
// implementation. That is the whole point of pinning a wire contract: a test
// that derives its expectation from the code it tests agrees with the code
// even when the code is wrong.

// stubCfg is a config for a stub server. The token is obviously fake and is
// long enough to clear MinTokenLen.
func stubCfg(url string) Config {
	return Config{URL: url, Token: "test-token-not-a-real-credential"}
}

// The list route's envelope is parsed, in the server's order, with every field
// landing in the right place. created_at is epoch MILLISECONDS.
func TestRowsParsesTheEnvelope(t *testing.T) {
	var gotPath, gotAuth, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"clips":[` +
			`{"id":7,"body":"newest","device":"phone","created_at":1700000002000,"bump_count":2},` +
			`{"id":4,"body":"older","device":"desktop","created_at":1700000001000,"bump_count":0}` +
			`],"limit":100}`))
	}))
	defer srv.Close()

	clips, err := New(stubCfg(srv.URL)).Rows(0)
	if err != nil {
		t.Fatalf("Rows: %v", err)
	}
	if gotPath != "/v1/clips?limit=100" {
		t.Errorf("request path = %q, want /v1/clips?limit=100 (the default limit)", gotPath)
	}
	if gotAuth != "Bearer test-token-not-a-real-credential" {
		t.Errorf("Authorization = %q — the bearer header is the only auth this API takes", gotAuth)
	}
	if gotAccept != "application/json" {
		t.Errorf("Accept = %q", gotAccept)
	}
	if len(clips) != 2 {
		t.Fatalf("got %d clips, want 2: %+v", len(clips), clips)
	}
	// 🔴 THE SERVER'S ORDER, VERBATIM. Newest-first is the server's job and
	// the client must not re-sort it.
	if clips[0].ID != 7 || clips[1].ID != 4 {
		t.Errorf("order = %d,%d — want 7,4 exactly as served", clips[0].ID, clips[1].ID)
	}
	if clips[0].Body != "newest" || clips[0].Device != "phone" ||
		clips[0].CreatedAt != 1700000002000 || clips[0].BumpCount != 2 {
		t.Errorf("row 0 = %+v", clips[0])
	}
}

// An explicit limit is passed through rather than replaced by the default.
func TestRowsPassesTheLimit(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		_, _ = w.Write([]byte(`{"clips":[],"limit":5}`))
	}))
	defer srv.Close()
	if _, err := New(stubCfg(srv.URL)).Rows(5); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/clips?limit=5" {
		t.Errorf("path = %q, want limit=5", gotPath)
	}
}

// 🔴 A 200 THAT CARRIED NO ROWS IS NOT AN ERROR, AND AN ERROR IS NOT AN EMPTY
// LIST. These are the two halves of the same property and the picker renders
// them differently; this pins both in one place so neither can drift into the
// other.
func TestAnEmptyStoreAndAnUnreadableStoreAreDifferent(t *testing.T) {
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"clips":[],"limit":100}`))
	}))
	defer empty.Close()
	clips, err := New(stubCfg(empty.URL)).Rows(0)
	if err != nil {
		t.Fatalf("an empty store must not be an error: %v", err)
	}
	if len(clips) != 0 {
		t.Fatalf("got %d clips from an empty store", len(clips))
	}

	// Unreachable: a closed listener. The error must name the cause.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	clips, err = New(stubCfg(deadURL)).Rows(0)
	if err == nil {
		t.Fatal("an unreachable store must be an error, never an empty list")
	}
	if clips != nil {
		t.Errorf("a failed fetch handed back %+v, want nil", clips)
	}
	if !strings.Contains(err.Error(), "unreachable") {
		t.Errorf("message = %q, want it to say the store is unreachable", err)
	}
	if !strings.Contains(err.Error(), "refused") && !strings.Contains(err.Error(), "connect") {
		t.Errorf("message = %q, want it to carry the transport cause", err)
	}
}

// 🔴 THE SERVICE'S OWN `detail` IS THE MESSAGE. It is a written-out sentence
// explaining the refusal and nothing this client could compose is better, so
// it is passed through verbatim rather than replaced by a status name.
func TestTheServicesDetailIsThePassedThroughMessage(t *testing.T) {
	const detail = "`limit` must be an integer in 1..500; it is not clamped"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_limit","detail":"` + detail + `"}`))
	}))
	defer srv.Close()
	_, err := New(stubCfg(srv.URL)).Rows(0)
	if err == nil {
		t.Fatal("a 400 must be an error")
	}
	if !strings.Contains(err.Error(), detail) {
		t.Errorf("message = %q, want the service's own detail %q verbatim", err, detail)
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("message = %q, want the status code too", err)
	}
}

// A 401 says the configured TOKEN is the problem — which is the one thing a
// 401 on this path can mean, and the operator needs to be sent at the right
// file.
func TestA401NamesTheTokenKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer srv.Close()
	_, err := New(stubCfg(srv.URL)).Rows(0)
	if err == nil {
		t.Fatal("a 401 must be an error")
	}
	if !strings.Contains(err.Error(), KeyToken) {
		t.Errorf("message = %q, want it to name %s", err, KeyToken)
	}
}

// 🔴 THE TOKEN IS NEVER IN A MESSAGE. A failure message may name the URL (the
// operator needs to know which endpoint failed); the credential must not
// appear anywhere in it.
func TestNoFailureMessageCarriesTheToken(t *testing.T) {
	const token = "s3cret-token-value-do-not-print"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"store_unreadable","detail":"the store could not be read"}`))
	}))
	defer srv.Close()
	cfg := Config{URL: srv.URL, Token: token}
	// POSITIVE CONTROL: the request really does carry the token, so a clean
	// message below is about redaction and not about the token being absent.
	var sawToken bool
	ctrl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawToken = r.Header.Get("Authorization") == "Bearer "+token
		_, _ = w.Write([]byte(`{"clips":[],"limit":100}`))
	}))
	defer ctrl.Close()
	if _, err := New(Config{URL: ctrl.URL, Token: token}).Rows(0); err != nil {
		t.Fatal(err)
	}
	if !sawToken {
		t.Fatal("the control never saw the token on the wire — this guard would pass vacuously")
	}

	_, err := New(cfg).Rows(0)
	if err == nil {
		t.Fatal("a 500 must be an error")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("the failure message leaks the token: %q", err)
	}
}

// A 200 carrying something that is not the list envelope is an error, not an
// empty list — the same property as the transport failure above, at the parse
// layer. A bare ARRAY is the specific shape worth pinning: it is what a
// different service would answer with, and reading it as zero clips would
// render as "you have no clips".
func TestAnUnparseableAnswerIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not json at all`))
	}))
	defer srv.Close()
	if _, err := New(stubCfg(srv.URL)).Rows(0); err == nil {
		t.Fatal("an unparseable 200 must be an error, never an empty list")
	}
}

// Search escapes the query and asks the SERVER — the one caller-visible
// difference from Rows. The picker does not use it; this pins the URL shape so
// the two semantics stay separable.
func TestSearchEscapesTheQuery(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		_, _ = w.Write([]byte(`{"clips":[],"limit":100,"query":"a b&c"}`))
	}))
	defer srv.Close()
	if _, err := New(stubCfg(srv.URL)).Search("a b&c", 0); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/clips?limit=100&q=a+b%26c" {
		t.Errorf("path = %q, want the query escaped", gotPath)
	}
}

// Age's units, pinned with LITERAL expectations. created_at is epoch millis;
// reading it as seconds renders every clip as decades old, so the boundary
// cases are the ones worth having.
func TestAge(t *testing.T) {
	const now = 1_700_000_000_000
	cases := []struct {
		name string
		at   int64
		want string
	}{
		{"just now", now, "0s"},
		{"59s", now - 59_000, "59s"},
		{"one minute", now - 60_000, "1m"},
		{"59m", now - 59*60_000, "59m"},
		{"one hour", now - 3_600_000, "1h"},
		{"23h", now - 23*3_600_000, "23h"},
		{"one day", now - 86_400_000, "1d"},
		{"nine days", now - 9*86_400_000, "9d"},
		// A clip stamped in the future is clock skew between two devices, not
		// an error the UI should branch on.
		{"future", now + 5_000, "0s"},
	}
	for _, c := range cases {
		if got := Age(c.at, now); got != c.want {
			t.Errorf("%s: Age = %q, want %q", c.name, got, c.want)
		}
	}
}
