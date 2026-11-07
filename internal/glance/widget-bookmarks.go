package glance

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

var bookmarksWidgetTemplate = mustParseTemplate("bookmarks.html", "widget-base.html")
var bookmarksCardsWidgetTemplate = mustParseTemplate("bookmarks-cards.html", "widget-base.html", "bookmark-card.html")
var bookmarkCardTemplate = mustParseTemplate("bookmark-card.html")

var bookmarksIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

const (
	bookmarksMaxRequestBodySize = 16 * 1024
	bookmarksMaxTitleLength     = 200
	bookmarksMaxDescriptionSize = 1000
	bookmarksMaxURLLength       = 2048
)

type bookmarkLink struct {
	Title       string          `yaml:"title"`
	URL         string          `yaml:"url"`
	Description string          `yaml:"description"`
	Icon        customIconField `yaml:"icon"`
	// we need a pointer to bool to know whether a value was provided,
	// however there's no way to dereference a pointer in a template so
	// {{ if not .SameTab }} would return true for any non-nil pointer
	// which leaves us with no way of checking if the value is true or
	// false, hence the duplicated fields below
	SameTabRaw   *bool  `yaml:"same-tab"`
	SameTab      bool   `yaml:"-"`
	HideArrowRaw *bool  `yaml:"hide-arrow"`
	HideArrow    bool   `yaml:"-"`
	Target       string `yaml:"target"`
}

type bookmarkGroup struct {
	Title     string         `yaml:"title"`
	Color     *hslColorField `yaml:"color"`
	SameTab   bool           `yaml:"same-tab"`
	HideArrow bool           `yaml:"hide-arrow"`
	Target    string         `yaml:"target"`
	Links     []bookmarkLink `yaml:"links"`
}

// prepareLink applies the group defaults to a link. Used both for links
// defined in the config and for links added through the web interface.
func (group *bookmarkGroup) prepareLink(link *bookmarkLink) {
	if link.SameTabRaw == nil {
		link.SameTab = group.SameTab
	} else {
		link.SameTab = *link.SameTabRaw
	}

	if link.HideArrowRaw == nil {
		link.HideArrow = group.HideArrow
	} else {
		link.HideArrow = *link.HideArrowRaw
	}

	if link.Target == "" {
		if group.Target != "" {
			link.Target = group.Target
		} else {
			if link.SameTab {
				link.Target = ""
			} else {
				link.Target = "_blank"
			}
		}
	}
}

type bookmarksWidget struct {
	widgetBase  `yaml:",inline"`
	cachedHTML  template.HTML   `yaml:"-"`
	Style       string          `yaml:"style"`
	BookmarksID string          `yaml:"id"`
	AllowAdd    bool            `yaml:"allow-add"`
	Groups      []bookmarkGroup `yaml:"groups"`
	configPath  string          `yaml:"-"`
}

func (widget *bookmarksWidget) initialize() error {
	widget.withTitle("Bookmarks").withError(nil)

	if widget.AllowAdd {
		if widget.Style != "cards" {
			return errors.New("allow-add is only supported with style: cards")
		}

		if widget.BookmarksID == "" {
			return errors.New("id is required when allow-add is enabled")
		}

		if !bookmarksIDPattern.MatchString(widget.BookmarksID) {
			return errors.New("id may only contain letters, numbers, dashes and underscores")
		}
	}

	for g := range widget.Groups {
		group := &widget.Groups[g]
		for l := range group.Links {
			group.prepareLink(&group.Links[l])
		}
	}

	widget.rerender()
	return nil
}

func (widget *bookmarksWidget) Render() template.HTML {
	return widget.cachedHTML
}

func (widget *bookmarksWidget) rerender() {
	if widget.Style == "cards" {
		widget.cachedHTML = widget.renderTemplate(widget, bookmarksCardsWidgetTemplate)
	} else {
		widget.cachedHTML = widget.renderTemplate(widget, bookmarksWidgetTemplate)
	}
}

// bookmarkLinkEntry is the format in which bookmarks added through the web
// interface are written to the config file.
type bookmarkLinkEntry struct {
	Title       string `yaml:"title"`
	URL         string `yaml:"url"`
	Description string `yaml:"description,omitempty"`
}

// appendBookmarkToConfig writes a bookmark to the YAML file that defines this
// widget. Comments and formatting of the rest of the file are preserved by
// inserting the new list item textually right after the last link of the group.
func (widget *bookmarksWidget) appendBookmarkToConfig(groupIndex int, link bookmarkLink) error {
	if widget.configPath == "" {
		return errors.New("the config file path is not available")
	}

	_, includes, err := parseYAMLIncludes(widget.configPath)
	if err != nil {
		return fmt.Errorf("reading config files: %w", err)
	}

	paths := make([]string, 0, len(includes)+1)
	paths = append(paths, widget.configPath)

	for path := range includes {
		if path != widget.configPath {
			paths = append(paths, path)
		}
	}

	sort.Strings(paths)

	for _, path := range paths {
		contents, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("reading %s: %w", path, err)
		}

		var document yaml.Node
		if err := yaml.Unmarshal(contents, &document); err != nil {
			// Files that are only meant to be included may not be valid YAML on
			// their own, skip over them.
			continue
		}

		groups := findBookmarksWidgetGroups(&document, widget.BookmarksID)
		if groups == nil {
			continue
		}

		if groupIndex < 0 || groupIndex >= len(groups.Content) {
			return errors.New("unknown group")
		}

		return insertBookmarkLinkIntoFile(path, contents, groups.Content[groupIndex], link)
	}

	return fmt.Errorf("could not find the bookmarks widget with id %q in the config files", widget.BookmarksID)
}

// findBookmarksWidgetGroups returns the "groups" sequence node of the bookmarks
// widget with the given id, if the given document contains it.
func findBookmarksWidgetGroups(node *yaml.Node, id string) *yaml.Node {
	if node.Kind == yaml.MappingNode {
		var widgetType, widgetID string
		var groups *yaml.Node

		for i := 0; i+1 < len(node.Content); i += 2 {
			switch node.Content[i].Value {
			case "type":
				widgetType = node.Content[i+1].Value
			case "id":
				widgetID = node.Content[i+1].Value
			case "groups":
				groups = node.Content[i+1]
			}
		}

		if widgetType == "bookmarks" && widgetID == id && groups != nil && groups.Kind == yaml.SequenceNode {
			return groups
		}
	}

	for _, child := range node.Content {
		if groups := findBookmarksWidgetGroups(child, id); groups != nil {
			return groups
		}
	}

	return nil
}

func insertBookmarkLinkIntoFile(path string, contents []byte, groupNode *yaml.Node, link bookmarkLink) error {
	if groupNode.Kind != yaml.MappingNode || groupNode.Style&yaml.FlowStyle != 0 {
		return errors.New("cannot add bookmark: the group must use block style")
	}

	var linksKeyNode, linksNode *yaml.Node

	for i := 0; i+1 < len(groupNode.Content); i += 2 {
		if groupNode.Content[i].Value == "links" {
			linksKeyNode = groupNode.Content[i]
			linksNode = groupNode.Content[i+1]
			break
		}
	}

	if linksKeyNode == nil || linksNode == nil {
		return errors.New("cannot add bookmark: the group has no links property")
	}

	// An empty "links:" property is decoded as a null scalar rather than an
	// empty sequence.
	isEmptyList := linksNode.Kind == yaml.ScalarNode && linksNode.Tag == "!!null"

	if !isEmptyList && linksNode.Kind != yaml.SequenceNode {
		return errors.New("cannot add bookmark: the links property is not a list")
	}

	updated, err := insertYAMLSequenceItem(contents, linksKeyNode, linksNode, link)
	if err != nil {
		return err
	}

	mode := os.FileMode(0o644)
	if stat, statErr := os.Stat(path); statErr == nil {
		mode = stat.Mode().Perm()
	}

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, updated, mode); err != nil {
		return err
	}

	return os.Rename(tmpPath, path)
}

func insertYAMLSequenceItem(contents []byte, linksKeyNode, linksNode *yaml.Node, link bookmarkLink) ([]byte, error) {
	if linksNode.Kind == yaml.SequenceNode && linksNode.Style&yaml.FlowStyle != 0 && len(linksNode.Content) > 0 {
		return nil, errors.New("cannot add bookmark: the links list must use block style")
	}

	lines := strings.Split(string(contents), "\n")
	linksLineIndex := linksKeyNode.Line - 1

	if linksLineIndex < 0 || linksLineIndex >= len(lines) {
		return nil, errors.New("cannot add bookmark: the links position is out of range")
	}

	linksLine := lines[linksLineIndex]
	linksIndent := countLeadingWhitespace(linksLine)

	// An empty inline list ("links: []") has to be converted to block style,
	// otherwise items can't be appended to it.
	if colonIndex := strings.Index(linksLine, ":"); colonIndex != -1 {
		rest := strings.TrimSpace(linksLine[colonIndex+1:])
		if rest != "" && !strings.HasPrefix(rest, "#") {
			if rest != "[]" {
				return nil, errors.New("cannot add bookmark: the links list must use block style")
			}
			lines[linksLineIndex] = strings.TrimRight(linksLine[:colonIndex+1], " \t")
		}
	}

	itemIndent := -1
	insertAt := -1
	existingItems := false

	for i := linksLineIndex + 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])

		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		indent := countLeadingWhitespace(lines[i])
		isSequenceItem := trimmed == "-" || strings.HasPrefix(trimmed, "- ")

		if itemIndent == -1 {
			if isSequenceItem && indent > linksIndent {
				itemIndent = indent
				existingItems = true
				continue
			}

			// The links list is empty, insert before the next key or group.
			itemIndent = linksIndent + 2
			insertAt = i
			break
		}

		if indent > itemIndent || (indent == itemIndent && isSequenceItem) {
			continue
		}

		insertAt = i
		break
	}

	if itemIndent == -1 {
		itemIndent = linksIndent + 2
	}

	if insertAt == -1 {
		insertAt = len(lines)
		if insertAt > 0 && lines[insertAt-1] == "" {
			insertAt--
		}
	}

	// Insert the new item right after the last link (rather than right before
	// the next block) so that the blank line separating the group from what
	// follows stays where it is.
	blankLineBefore := false

	if existingItems {
		lastContent := insertAt - 1

		for lastContent > linksLineIndex {
			trimmed := strings.TrimSpace(lines[lastContent])
			isComment := strings.HasPrefix(trimmed, "#") && countLeadingWhitespace(lines[lastContent]) >= itemIndent

			if trimmed != "" && !isComment {
				break
			}

			lastContent--
		}

		insertAt = lastContent + 1
		blankLineBefore = true
	}

	encoded, err := yaml.Marshal([]bookmarkLinkEntry{{
		Title:       link.Title,
		URL:         link.URL,
		Description: link.Description,
	}})
	if err != nil {
		return nil, err
	}

	prefix := strings.Repeat(" ", itemIndent)
	newLines := strings.Split(strings.TrimRight(string(encoded), "\n"), "\n")

	for i := range newLines {
		newLines[i] = prefix + newLines[i]
	}

	if blankLineBefore {
		newLines = append([]string{""}, newLines...)
	}

	result := make([]string, 0, len(lines)+len(newLines))
	result = append(result, lines[:insertAt]...)
	result = append(result, newLines...)
	result = append(result, lines[insertAt:]...)

	return []byte(strings.Join(result, "\n")), nil
}

func countLeadingWhitespace(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] != ' ' && s[i] != '\t' {
			return i
		}
	}

	return len(s)
}

// markConfigSelfWrite remembers the hash of the config contents Glance just
// wrote, so that the file watcher can skip reloading (and refetching every
// widget) in response to Glance's own writes.
func (widget *bookmarksWidget) markConfigSelfWrite() {
	if widget.configPath == "" {
		return
	}

	contents, _, err := parseYAMLIncludes(widget.configPath)
	if err != nil {
		slog.Warn("Failed to read the config after writing a bookmark", "error", err)
		return
	}

	hash := sha256.Sum256(contents)
	configSelfWriteContentsHash.Store(&hash)
}

// handleRequest handles POST /api/widgets/{widget}/bookmarks, which adds a
// bookmark to one of the widget's groups and returns the rendered card.
func (widget *bookmarksWidget) handleRequest(w http.ResponseWriter, r *http.Request) {
	if strings.Trim(r.PathValue("path"), "/") != "bookmarks" {
		http.NotFound(w, r)
		return
	}

	if !widget.AllowAdd {
		http.Error(w, "adding bookmarks is not enabled for this widget", http.StatusForbidden)
		return
	}

	var payload struct {
		GroupIndex  int    `json:"group-index"`
		Title       string `json:"title"`
		URL         string `json:"url"`
		Description string `json:"description"`
	}

	body := http.MaxBytesReader(w, r.Body, bookmarksMaxRequestBodySize)
	if err := json.NewDecoder(body).Decode(&payload); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	title := strings.TrimSpace(payload.Title)
	rawURL := strings.TrimSpace(payload.URL)
	description := strings.TrimSpace(payload.Description)

	if title == "" {
		http.Error(w, "title is required", http.StatusBadRequest)
		return
	}

	if len([]rune(title)) > bookmarksMaxTitleLength {
		http.Error(w, fmt.Sprintf("title is longer than %d characters", bookmarksMaxTitleLength), http.StatusBadRequest)
		return
	}

	if len([]rune(description)) > bookmarksMaxDescriptionSize {
		http.Error(w, fmt.Sprintf("description is longer than %d characters", bookmarksMaxDescriptionSize), http.StatusBadRequest)
		return
	}

	if len(rawURL) > bookmarksMaxURLLength {
		http.Error(w, fmt.Sprintf("url is longer than %d characters", bookmarksMaxURLLength), http.StatusBadRequest)
		return
	}

	parsedURL, err := url.Parse(rawURL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" {
		http.Error(w, "url must be a valid http or https URL", http.StatusBadRequest)
		return
	}

	if payload.GroupIndex < 0 || payload.GroupIndex >= len(widget.Groups) {
		http.Error(w, "unknown group", http.StatusBadRequest)
		return
	}

	group := &widget.Groups[payload.GroupIndex]
	link := bookmarkLink{
		Title:       title,
		URL:         rawURL,
		Description: description,
	}
	group.prepareLink(&link)

	cardHTML, err := executeTemplateToString(bookmarkCardTemplate, link)
	if err != nil {
		slog.Error("Failed to render bookmark card", "error", err)
		http.Error(w, "failed to render bookmark", http.StatusInternalServerError)
		return
	}

	if err := widget.appendBookmarkToConfig(payload.GroupIndex, link); err != nil {
		slog.Error("Failed to write bookmark to the config file", "error", err, "config", widget.configPath)
		http.Error(w, "failed to save bookmark: "+err.Error(), http.StatusInternalServerError)
		return
	}

	widget.markConfigSelfWrite()

	group.Links = append(group.Links, link)
	widget.rerender()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(cardHTML))
}
