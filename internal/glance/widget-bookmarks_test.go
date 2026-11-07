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
	request := httptest.NewRequest(http.MethodPost, "/api/widgets/test/"+path, strings.NewReader(body))
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
