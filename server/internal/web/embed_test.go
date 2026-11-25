package web

import (
	"encoding/xml"
	"mime"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"testing"
)

// Exercise the file server, not the source directory: the release binary must
// serve all the browser assets without a font service, CDN, or working tree.
func TestEmbeddedWebAssets(t *testing.T) {
	h := Handler()
	get := func(path string, wantTypes ...string) []byte {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		mediaType, _, err := mime.ParseMediaType(w.Header().Get("Content-Type"))
		if err != nil || w.Code != http.StatusOK || !slices.Contains(wantTypes, mediaType) || w.Body.Len() == 0 {
			t.Fatalf("GET %s: status=%d type=%q bytes=%d", path, w.Code, mediaType, w.Body.Len())
		}
		return w.Body.Bytes()
	}
	page := get("/", "text/html")
	get("/app.css", "text/css")
	get("/material-icons.LICENSE.txt", "text/plain")
	script := get("/app.js", "text/javascript", "application/javascript")
	var sprite struct {
		Symbols []struct {
			ID   string `xml:"id,attr"`
			Path struct {
				D string `xml:"d,attr"`
			} `xml:"path"`
		} `xml:"symbol"`
	}
	if err := xml.Unmarshal(get("/material-icons.svg", "image/svg+xml"), &sprite); err != nil {
		t.Fatal(err)
	}
	ids := make(map[string]bool)
	for _, symbol := range sprite.Symbols {
		if symbol.ID == "" || symbol.Path.D == "" || ids[symbol.ID] {
			t.Fatalf("invalid or duplicate symbol %q", symbol.ID)
		}
		ids[symbol.ID] = true
	}
	refs := regexp.MustCompile(`href="/material-icons\.svg#([\w-]+)"`).FindAllSubmatch(page, -1)
	if len(refs) == 0 {
		t.Fatal("no embedded icon references on page")
	}
	for _, ref := range refs {
		if !ids[string(ref[1])] {
			t.Errorf("missing SVG symbol %q referenced in index.html", ref[1])
		}
	}
	jsRefs := regexp.MustCompile(`(?:icon|cardAction)\(\s*'([\w-]+)'`).FindAllSubmatch(script, -1)
	if len(jsRefs) == 0 {
		t.Fatal("no icon references found in app.js")
	}
	for _, ref := range jsRefs {
		if !ids[string(ref[1])] {
			t.Errorf("missing SVG symbol %q referenced in app.js", ref[1])
		}
	}
}
