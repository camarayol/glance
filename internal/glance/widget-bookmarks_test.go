package glance

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const bookmarksTestConfig = `pages:
  - name: Test
    columns:
      - size: full
        widgets:
          - type: bookmarks
            style: cards
            id: test
            allow-add: true
            groups:
              - title: Group
                links:
                  - title: Existing
                    url: https://existing.example.com/
              - title: Second
                links:
                  - title: Existing 2
                    url: https://existing2.example.com/
`

func newTestBookmarksWidget(t *testing.T, configPath string) *bookmarksWidget {
	t.Helper()

	widget := &bookmarksWidget{
		Style:       "cards",
		BookmarksID: "test",
		AllowAdd:    true,
		Groups: []bookmarkGroup{
			{
				Title: "Group",
				Links: []bookmarkLink{
					{Title: "Existing", URL: "https://existing.example.com/"},
				},
			},
			{
				Title: "Second",
				Links: []bookmarkLink{
					{Title: "Existing 2", URL: "https://existing2.example.com/"},
				},
			},
		},
		configPath: configPath,
	}

	widget.setID(1)

	if err := widget.initialize(); err != nil {
		t.Fatalf("initializing widget: %v", err)
	}

	return widget
}

func writeTestConfig(t *testing.T, contents string) string {
	t.Helper()

	configPath := filepath.Join(t.TempDir(), "glance.yml")
	if err := os.WriteFile(configPath, []byte(contents), 0o644); err != nil {
		t.Fatalf("writing test config: %v", err)
	}

	return configPath
}

func bookmarksAddRequest(path string, body string) *http.Request {
	return bookmarksRequest(http.MethodPost, path, body)
}

func bookmarksRequest(method, path, body string) *http.Request {
	request := httptest.NewRequest(method, "/api/widgets/test/"+path, strings.NewReader(body))
	request.SetPathValue("path", path)
	return request
}

func parseBookmarksWidgetFromConfig(t *testing.T, configPath string) *bookmarksWidget {
	t.Helper()

	contents, _, err := parseYAMLIncludes(configPath)
	if err != nil {
		t.Fatalf("parsing config includes: %v", err)
	}

	config, err := newConfigFromYAML(contents)
	if err != nil {
		t.Fatalf("parsing config: %v", err)
	}

	bookmarks, ok := config.Pages[0].Columns[0].Widgets[0].(*bookmarksWidget)
	if !ok {
		t.Fatalf("expected a bookmarks widget, got %T", config.Pages[0].Columns[0].Widgets[0])
	}

	return bookmarks
}

func TestBookmarksWidgetAddWritesToConfig(t *testing.T) {
	configPath := writeTestConfig(t, bookmarksTestConfig)
	widget := newTestBookmarksWidget(t, configPath)
	original, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading test config: %v", err)
	}

	response := httptest.NewRecorder()
	widget.handleRequest(response, bookmarksAddRequest(
		"bookmarks",
		`{"group-index": 0, "title": "GitHub", "url": "https://github.com/", "description": "Code hosting"}`,
	))

	if response.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", response.Code, response.Body.String())
	}

	if len(widget.Groups[0].Links) != 2 {
		t.Fatalf("expected 2 links in memory, got %d", len(widget.Groups[0].Links))
	}

	if !strings.Contains(response.Body.String(), "bookmarks-card") {
		t.Errorf("expected the response to contain a bookmark card, got %q", response.Body.String())
	}

	contents, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading updated config: %v", err)
	}

	if string(contents) == string(original) {
		t.Fatal("expected the config file to be modified")
	}

	if !strings.Contains(string(contents), "title: GitHub") || !strings.Contains(string(contents), "description: Code hosting") {
		t.Errorf("expected the new bookmark in the config, got:\n%s", contents)
	}

	// The file must remain valid and the new link must land in the right group.
	reloaded := parseBookmarksWidgetFromConfig(t, configPath)

	if len(reloaded.Groups[0].Links) != 2 {
		t.Fatalf("expected 2 links in the first group, got %d", len(reloaded.Groups[0].Links))
	}

	if added := reloaded.Groups[0].Links[1]; added.Title != "GitHub" || added.Description != "Code hosting" {
		t.Errorf("unexpected reloaded link: %+v", added)
	}

	if len(reloaded.Groups[1].Links) != 1 {
		t.Errorf("expected the second group to be untouched, got %d links", len(reloaded.Groups[1].Links))
	}
}

func TestBookmarksWidgetAddToIncludedFile(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "glance.yml")
	includePath := filepath.Join(dir, "bookmarks.yml")

	mainConfig := `pages:
  - name: Test
    columns:
      - size: full
        widgets:
          - $include: bookmarks.yml
`
	includeConfig := `- type: bookmarks
  style: cards
  id: test
  allow-add: true
  groups:
    - title: Group
      links:
        - title: Existing
          url: https://existing.example.com/
    - title: Second
      links:
        - title: Existing 2
          url: https://existing2.example.com/
`

	if err := os.WriteFile(mainPath, []byte(mainConfig), 0o644); err != nil {
		t.Fatalf("writing main config: %v", err)
	}

	if err := os.WriteFile(includePath, []byte(includeConfig), 0o644); err != nil {
		t.Fatalf("writing included config: %v", err)
	}

	widget := newTestBookmarksWidget(t, mainPath)

	response := httptest.NewRecorder()
	widget.handleRequest(response, bookmarksAddRequest(
		"bookmarks",
		`{"group-index": 1, "title": "Included", "url": "https://included.example.com/"}`,
	))

	if response.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", response.Code, response.Body.String())
	}

	mainContents, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatalf("reading main config: %v", err)
	}

	if string(mainContents) != mainConfig {
		t.Error("expected the main config file to be left untouched")
	}

	includeContents, err := os.ReadFile(includePath)
	if err != nil {
		t.Fatalf("reading included config: %v", err)
	}

	if !strings.Contains(string(includeContents), "title: Included") {
		t.Errorf("expected the bookmark to be written to the included file, got:\n%s", includeContents)
	}

	reloaded := parseBookmarksWidgetFromConfig(t, mainPath)

	if len(reloaded.Groups[1].Links) != 2 || reloaded.Groups[1].Links[1].Title != "Included" {
		t.Errorf("unexpected links in the second group: %+v", reloaded.Groups[1].Links)
	}
}

func TestBookmarksWidgetAddValidation(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"missing title", `{"group-index": 0, "title": " ", "url": "https://example.com/"}`},
		{"missing url", `{"group-index": 0, "title": "Example", "url": ""}`},
		{"invalid url", `{"group-index": 0, "title": "Example", "url": "javascript:alert(1)"}`},
		{"url without host", `{"group-index": 0, "title": "Example", "url": "https://"}`},
		{"unknown group", `{"group-index": 3, "title": "Example", "url": "https://example.com/"}`},
		{"invalid body", `{"group-index": 0,`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configPath := writeTestConfig(t, bookmarksTestConfig)
			original, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatalf("reading test config: %v", err)
			}

			widget := newTestBookmarksWidget(t, configPath)

			response := httptest.NewRecorder()
			widget.handleRequest(response, bookmarksAddRequest("bookmarks", test.body))

			if response.Code != http.StatusBadRequest {
				t.Errorf("expected status 400, got %d", response.Code)
			}

			if len(widget.Groups[0].Links) != 1 {
				t.Errorf("expected no link to be added, got %d links", len(widget.Groups[0].Links))
			}

			contents, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatalf("reading test config: %v", err)
			}

			if string(contents) != string(original) {
				t.Error("expected the config file to be left untouched")
			}
		})
	}
}

func TestBookmarksWidgetAddDisabled(t *testing.T) {
	configPath := writeTestConfig(t, bookmarksTestConfig)
	widget := newTestBookmarksWidget(t, configPath)
	widget.AllowAdd = false

	response := httptest.NewRecorder()
	widget.handleRequest(response, bookmarksAddRequest(
		"bookmarks",
		`{"group-index": 0, "title": "GitHub", "url": "https://github.com/"}`,
	))

	if response.Code != http.StatusForbidden {
		t.Errorf("expected status 403, got %d", response.Code)
	}
}

func TestBookmarksWidgetUnknownPath(t *testing.T) {
	configPath := writeTestConfig(t, bookmarksTestConfig)
	widget := newTestBookmarksWidget(t, configPath)

	response := httptest.NewRecorder()
	widget.handleRequest(response, bookmarksAddRequest("unknown", `{}`))

	if response.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", response.Code)
	}
}

func TestBookmarksWidgetAddUnsupportedFormatting(t *testing.T) {
	configPath := writeTestConfig(t, `pages:
  - name: Test
    columns:
      - size: full
        widgets:
          - type: bookmarks
            style: cards
            id: test
            allow-add: true
            groups:
              - title: Group
                links: [{ title: Existing, url: "https://existing.example.com/" }]
`)
	original, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading test config: %v", err)
	}

	widget := newTestBookmarksWidget(t, configPath)

	response := httptest.NewRecorder()
	widget.handleRequest(response, bookmarksAddRequest(
		"bookmarks",
		`{"group-index": 0, "title": "GitHub", "url": "https://github.com/"}`,
	))

	if response.Code != http.StatusInternalServerError {
		t.Errorf("expected status 500, got %d", response.Code)
	}

	contents, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading test config: %v", err)
	}

	if string(contents) != string(original) {
		t.Error("expected the config file to be left untouched")
	}
}

func TestBookmarksWidgetAddMissingWidgetInConfig(t *testing.T) {
	configPath := writeTestConfig(t, bookmarksTestConfig)
	widget := newTestBookmarksWidget(t, configPath)
	widget.BookmarksID = "another"

	response := httptest.NewRecorder()
	widget.handleRequest(response, bookmarksAddRequest(
		"bookmarks",
		`{"group-index": 0, "title": "GitHub", "url": "https://github.com/"}`,
	))

	if response.Code != http.StatusInternalServerError {
		t.Errorf("expected status 500, got %d", response.Code)
	}
}

func TestBookmarksWidgetAddEmptyLinksList(t *testing.T) {
	configPath := writeTestConfig(t, `pages:
  - name: Test
    columns:
      - size: full
        widgets:
          - type: bookmarks
            style: cards
            id: test
            allow-add: true
            groups:
              - title: Group
                links: []
`)
	widget := &bookmarksWidget{
		Style:       "cards",
		BookmarksID: "test",
		AllowAdd:    true,
		Groups:      []bookmarkGroup{{Title: "Group"}},
		configPath:  configPath,
	}
	widget.setID(1)

	if err := widget.initialize(); err != nil {
		t.Fatalf("initializing widget: %v", err)
	}

	response := httptest.NewRecorder()
	widget.handleRequest(response, bookmarksAddRequest(
		"bookmarks",
		`{"group-index": 0, "title": "GitHub", "url": "https://github.com/"}`,
	))

	if response.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", response.Code, response.Body.String())
	}

	reloaded := parseBookmarksWidgetFromConfig(t, configPath)

	if len(reloaded.Groups[0].Links) != 1 || reloaded.Groups[0].Links[0].Title != "GitHub" {
		t.Errorf("unexpected links: %+v", reloaded.Groups[0].Links)
	}
}

func TestBookmarksWidgetInitializeErrors(t *testing.T) {
	tests := []struct {
		name   string
		widget bookmarksWidget
	}{
		{
			name:   "allow-add requires id",
			widget: bookmarksWidget{Style: "cards", AllowAdd: true},
		},
		{
			name:   "allow-add requires cards style",
			widget: bookmarksWidget{BookmarksID: "test", AllowAdd: true},
		},
		{
			name:   "id has invalid characters",
			widget: bookmarksWidget{Style: "cards", BookmarksID: "../escape", AllowAdd: true},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			widget := test.widget
			if err := widget.initialize(); err == nil {
				t.Error("expected an error, got nil")
			}
		})
	}
}

const bookmarksEditTestConfig = `pages:
  - name: Test
    columns:
      - size: full
        widgets:
          - type: bookmarks
            style: cards
            id: test
            allow-add: true
            groups:
              - title: Group
                links:
                  - title: First
                    url: https://first.example.com/

                  - title: Middle  # keep me
                    url: https://middle.example.com/
                    description: middle desc
                    icon: si:github
                    same-tab: true

                  - title: Last
                    url: https://last.example.com/
`

func newEditTestWidget(t *testing.T) (*bookmarksWidget, string) {
	t.Helper()

	configPath := writeTestConfig(t, bookmarksEditTestConfig)
	widget := parseBookmarksWidgetFromConfig(t, configPath)
	widget.configPath = configPath

	return widget, configPath
}

func TestBookmarksWidgetUpdateBookmark(t *testing.T) {
	widget, configPath := newEditTestWidget(t)

	response := httptest.NewRecorder()
	widget.handleRequest(response, bookmarksRequest(
		http.MethodPut,
		"bookmarks/0/1",
		`{"title": "Updated", "url": "https://updated.example.com/", "description": "new description"}`,
	))

	if response.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", response.Code, response.Body.String())
	}

	if !strings.Contains(response.Body.String(), ">Updated<") {
		t.Errorf("expected the response to contain the updated card, got %q", response.Body.String())
	}

	contents, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading updated config: %v", err)
	}

	config := string(contents)

	for _, want := range []string{
		"title: Updated",
		"url: https://updated.example.com/",
		"description: new description",
		"icon: si:github",
		"same-tab: true",
		"# keep me",
		"title: First",
		"title: Last",
	} {
		if !strings.Contains(config, want) {
			t.Errorf("expected the config to contain %q, got:\n%s", want, config)
		}
	}

	if strings.Contains(config, "Middle") || strings.Contains(config, "middle desc") {
		t.Errorf("expected the old values to be replaced, got:\n%s", config)
	}

	reloaded := parseBookmarksWidgetFromConfig(t, configPath)

	if len(reloaded.Groups[0].Links) != 3 {
		t.Fatalf("expected 3 links, got %d", len(reloaded.Groups[0].Links))
	}

	link := reloaded.Groups[0].Links[1]

	if link.Title != "Updated" || link.URL != "https://updated.example.com/" || link.Description != "new description" {
		t.Errorf("unexpected reloaded link: %+v", link)
	}

	if !link.SameTab || link.Target != "" {
		t.Errorf("expected the link's same-tab option to be preserved, got: %+v", link)
	}

	// The in-memory widget must be updated as well, without losing its options.
	inMemory := widget.Groups[0].Links[1]

	if inMemory.Title != "Updated" || inMemory.Description != "new description" {
		t.Errorf("unexpected in-memory link: %+v", inMemory)
	}

	if !inMemory.SameTab || inMemory.Target != "" {
		t.Errorf("expected the in-memory link's options to be preserved: %+v", inMemory)
	}
}

func TestBookmarksWidgetUpdateAddsDescription(t *testing.T) {
	widget, configPath := newEditTestWidget(t)

	response := httptest.NewRecorder()
	widget.handleRequest(response, bookmarksRequest(
		http.MethodPut,
		"bookmarks/0/0",
		`{"title": "First", "url": "https://first.example.com/", "description": "added description"}`,
	))

	if response.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", response.Code, response.Body.String())
	}

	reloaded := parseBookmarksWidgetFromConfig(t, configPath)
	link := reloaded.Groups[0].Links[0]

	if link.Description != "added description" {
		t.Errorf("expected the description to be added, got: %+v", link)
	}

	if link.URL != "https://first.example.com/" || reloaded.Groups[0].Links[1].Title != "Middle" {
		t.Errorf("expected the other links to be untouched, got: %+v", reloaded.Groups[0].Links)
	}

	contents, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading updated config: %v", err)
	}

	addedAt := strings.Index(string(contents), "description: added description")
	middleAt := strings.Index(string(contents), "title: Middle")

	if addedAt == -1 || middleAt == -1 || addedAt > middleAt {
		t.Errorf("expected the description to be inserted into the first link, got:\n%s", contents)
	}
}

func TestBookmarksWidgetUpdateRemovesDescription(t *testing.T) {
	widget, configPath := newEditTestWidget(t)

	response := httptest.NewRecorder()
	widget.handleRequest(response, bookmarksRequest(
		http.MethodPut,
		"bookmarks/0/1",
		`{"title": "Middle", "url": "https://middle.example.com/", "description": ""}`,
	))

	if response.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", response.Code, response.Body.String())
	}

	contents, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading updated config: %v", err)
	}

	if strings.Contains(string(contents), "description:") {
		t.Errorf("expected the description line to be removed, got:\n%s", contents)
	}

	reloaded := parseBookmarksWidgetFromConfig(t, configPath)

	if reloaded.Groups[0].Links[1].Description != "" {
		t.Errorf("expected the description to be removed, got: %+v", reloaded.Groups[0].Links[1])
	}

	if len(reloaded.Groups[0].Links) != 3 {
		t.Errorf("expected all links to remain, got %d", len(reloaded.Groups[0].Links))
	}
}

func TestBookmarksWidgetDeleteBookmark(t *testing.T) {
	widget, configPath := newEditTestWidget(t)

	response := httptest.NewRecorder()
	widget.handleRequest(response, bookmarksRequest(http.MethodDelete, "bookmarks/0/1", ""))

	if response.Code != http.StatusNoContent {
		t.Fatalf("expected status 204, got %d: %s", response.Code, response.Body.String())
	}

	contents, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading updated config: %v", err)
	}

	config := string(contents)

	if strings.Contains(config, "Middle") || strings.Contains(config, "middle.example.com") {
		t.Errorf("expected the bookmark to be removed, got:\n%s", config)
	}

	if !strings.Contains(config, "title: First") || !strings.Contains(config, "title: Last") {
		t.Errorf("expected the other bookmarks to remain, got:\n%s", config)
	}

	if strings.Contains(config, "\n\n\n") {
		t.Errorf("expected no leftover blank lines, got:\n%s", config)
	}

	reloaded := parseBookmarksWidgetFromConfig(t, configPath)

	if len(reloaded.Groups[0].Links) != 2 || reloaded.Groups[0].Links[0].Title != "First" || reloaded.Groups[0].Links[1].Title != "Last" {
		t.Errorf("unexpected links after delete: %+v", reloaded.Groups[0].Links)
	}

	if len(widget.Groups[0].Links) != 2 || widget.Groups[0].Links[1].Title != "Last" {
		t.Errorf("expected the in-memory links to be updated, got: %+v", widget.Groups[0].Links)
	}
}

func TestBookmarksWidgetUpdateAndDeleteUnknownBookmark(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		status int
	}{
		{"update unknown bookmark", http.MethodPut, "bookmarks/0/9", http.StatusNotFound},
		{"delete unknown bookmark", http.MethodDelete, "bookmarks/0/9", http.StatusNotFound},
		{"update unknown group", http.MethodPut, "bookmarks/9/0", http.StatusBadRequest},
		{"delete unknown group", http.MethodDelete, "bookmarks/9/0", http.StatusBadRequest},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			widget, configPath := newEditTestWidget(t)
			original, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatalf("reading test config: %v", err)
			}

			response := httptest.NewRecorder()
			widget.handleRequest(response, bookmarksRequest(
				test.method,
				test.path,
				`{"title": "Updated", "url": "https://updated.example.com/"}`,
			))

			if response.Code != test.status {
				t.Errorf("expected status %d, got %d", test.status, response.Code)
			}

			contents, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatalf("reading config: %v", err)
			}

			if string(contents) != string(original) {
				t.Error("expected the config file to be left untouched")
			}
		})
	}
}

func TestBookmarksWidgetMethodNotAllowed(t *testing.T) {
	tests := []struct {
		method string
		path   string
		allow  string
	}{
		{http.MethodPut, "bookmarks", http.MethodPost},
		{http.MethodDelete, "bookmarks", http.MethodPost},
		{http.MethodPost, "bookmarks/0/0", http.MethodPut + ", " + http.MethodDelete},
	}

	for _, test := range tests {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			widget, _ := newEditTestWidget(t)

			response := httptest.NewRecorder()
			widget.handleRequest(response, bookmarksRequest(test.method, test.path, "{}"))

			if response.Code != http.StatusMethodNotAllowed {
				t.Errorf("expected status 405, got %d", response.Code)
			}

			if allow := response.Header().Get("Allow"); allow != test.allow {
				t.Errorf("expected Allow header %q, got %q", test.allow, allow)
			}
		})
	}
}

func TestBookmarksWidgetEditDisabled(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
	}{
		{"update", http.MethodPut, "bookmarks/0/0"},
		{"delete", http.MethodDelete, "bookmarks/0/0"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			widget, _ := newEditTestWidget(t)
			widget.AllowAdd = false

			response := httptest.NewRecorder()
			widget.handleRequest(response, bookmarksRequest(
				test.method,
				test.path,
				`{"title": "Updated", "url": "https://updated.example.com/"}`,
			))

			if response.Code != http.StatusForbidden {
				t.Errorf("expected status 403, got %d", response.Code)
			}
		})
	}
}

func TestBookmarksWidgetEditFlowStyleBookmark(t *testing.T) {
	configPath := writeTestConfig(t, `pages:
  - name: Test
    columns:
      - size: full
        widgets:
          - type: bookmarks
            style: cards
            id: test
            allow-add: true
            groups:
              - title: Group
                links: [{ title: Existing, url: "https://existing.example.com/" }]
`)
	original, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading test config: %v", err)
	}

	widget := parseBookmarksWidgetFromConfig(t, configPath)
	widget.configPath = configPath

	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			response := httptest.NewRecorder()
			widget.handleRequest(response, bookmarksRequest(
				method,
				"bookmarks/0/0",
				`{"title": "Updated", "url": "https://updated.example.com/"}`,
			))

			if response.Code != http.StatusInternalServerError {
				t.Errorf("expected status 500, got %d", response.Code)
			}

			contents, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatalf("reading config: %v", err)
			}

			if string(contents) != string(original) {
				t.Error("expected the config file to be left untouched")
			}
		})
	}
}
