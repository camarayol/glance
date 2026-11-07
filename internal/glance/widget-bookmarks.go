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
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

var bookmarksWidgetTemplate = mustParseTemplate("bookmarks.html", "widget-base.html")
var bookmarksCardsWidgetTemplate = mustParseTemplate("bookmarks-cards.html", "widget-base.html", "bookmark-card.html")

// bookmarkCardTemplate is the card template of the cards widget's set, so that
// the card markup only has to be parsed once.
var bookmarkCardTemplate = bookmarksCardsWidgetTemplate.Lookup("bookmark-card.html")

var bookmarksIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// These allow the request handlers to tell a bad group or bookmark index apart
// from a failure that happened while saving.
var (
	errBookmarkGroupNotFound = errors.New("unknown group")
	errBookmarkNotFound      = errors.New("unknown bookmark")
)

const (
	bookmarksMaxRequestBodySize   = 16 * 1024
	bookmarksMaxTitleLength       = 200
	bookmarksMaxDescriptionLength = 1000
	bookmarksMaxURLLength         = 2048
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

// newLink builds a link from the request payload, applying the group defaults.
func (group *bookmarkGroup) newLink(payload bookmarkRequestPayload) bookmarkLink {
	link := bookmarkLink{
		Title:       payload.Title,
		URL:         payload.URL,
		Description: payload.Description,
	}
	group.prepareLink(&link)
	return link
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

// writeBookmarksConfig locates the config file that defines this widget,
// applies a change to it, writes the result back and records the write so that
// the config watcher does not reload the whole config in response to it.
// Comments and formatting of the rest of the file are preserved by the change
// functions, which edit the file textually instead of re-encoding the whole
// document.
func (widget *bookmarksWidget) writeBookmarksConfig(change func(contents []byte, groups *yaml.Node) ([]byte, error)) error {
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

		updated, err := change(contents, groups)
		if err != nil {
			return err
		}

		if err := writeConfigFile(path, updated); err != nil {
			return err
		}

		widget.markConfigSelfWrite()
		return nil
	}

	return fmt.Errorf("could not find the bookmarks widget with id %q in the config files", widget.BookmarksID)
}

// appendBookmarkToConfig inserts a bookmark right after the last link of the
// group.
func (widget *bookmarksWidget) appendBookmarkToConfig(groupIndex int, link bookmarkLink) error {
	return widget.writeBookmarksConfig(func(contents []byte, groups *yaml.Node) ([]byte, error) {
		linksKeyNode, linksNode, err := bookmarksGroupLinks(groups, groupIndex)
		if err != nil {
			return nil, err
		}

		return insertYAMLSequenceItem(contents, linksKeyNode, linksNode, link)
	})
}

func (widget *bookmarksWidget) updateBookmarkInConfig(groupIndex, linkIndex int, link bookmarkLink) error {
	return widget.writeBookmarksConfig(func(contents []byte, groups *yaml.Node) ([]byte, error) {
		linkNode, err := bookmarkLinkNode(groups, groupIndex, linkIndex)
		if err != nil {
			return nil, err
		}

		return updateBookmarkLinkInFile(contents, linkNode, link)
	})
}

func (widget *bookmarksWidget) deleteBookmarkFromConfig(groupIndex, linkIndex int) error {
	return widget.writeBookmarksConfig(func(contents []byte, groups *yaml.Node) ([]byte, error) {
		linkNode, err := bookmarkLinkNode(groups, groupIndex, linkIndex)
		if err != nil {
			return nil, err
		}

		return removeBookmarkLinkFromFile(contents, linkNode)
	})
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

// bookmarksGroupLinks returns the "links" key and value nodes of the group with
// the given index.
func bookmarksGroupLinks(groups *yaml.Node, groupIndex int) (*yaml.Node, *yaml.Node, error) {
	if groupIndex < 0 || groupIndex >= len(groups.Content) {
		return nil, nil, errBookmarkGroupNotFound
	}

	groupNode := groups.Content[groupIndex]

	if groupNode.Kind != yaml.MappingNode || groupNode.Style&yaml.FlowStyle != 0 {
		return nil, nil, errors.New("cannot modify bookmarks: the group must use block style")
	}

	for i := 0; i+1 < len(groupNode.Content); i += 2 {
		if groupNode.Content[i].Value != "links" {
			continue
		}

		linksNode := groupNode.Content[i+1]

		// An empty "links:" property is decoded as a null scalar rather than an
		// empty sequence.
		isEmptyList := linksNode.Kind == yaml.ScalarNode && linksNode.Tag == "!!null"

		if !isEmptyList && linksNode.Kind != yaml.SequenceNode {
			return nil, nil, errors.New("cannot modify bookmarks: the links property is not a list")
		}

		return groupNode.Content[i], linksNode, nil
	}

	return nil, nil, errors.New("cannot modify bookmarks: the group has no links property")
}

func bookmarkLinkNode(groups *yaml.Node, groupIndex, linkIndex int) (*yaml.Node, error) {
	_, linksNode, err := bookmarksGroupLinks(groups, groupIndex)
	if err != nil {
		return nil, err
	}

	if linksNode.Kind != yaml.SequenceNode || linkIndex < 0 || linkIndex >= len(linksNode.Content) {
		return nil, errBookmarkNotFound
	}

	return linksNode.Content[linkIndex], nil
}

func writeConfigFile(path string, contents []byte) error {
	mode := os.FileMode(0o644)
	if stat, err := os.Stat(path); err == nil {
		mode = stat.Mode().Perm()
	}

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, contents, mode); err != nil {
		return err
	}

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return err
	}

	return nil
}

// updateBookmarkLinkInFile rewrites only the lines that hold a changed value,
// which keeps the rest of the bookmark (extra properties, comments) intact.
func updateBookmarkLinkInFile(contents []byte, linkNode *yaml.Node, link bookmarkLink) ([]byte, error) {
	if linkNode.Kind != yaml.MappingNode || linkNode.Style&yaml.FlowStyle != 0 {
		return nil, errors.New("cannot edit bookmark: the bookmark must use block style")
	}

	lines := strings.Split(string(contents), "\n")
	lastLine := linkNode.Line

	replaced := make(map[int]string)
	descriptionLine := -1
	descriptionPresent := false

	for i := 0; i+1 < len(linkNode.Content); i += 2 {
		keyNode, valueNode := linkNode.Content[i], linkNode.Content[i+1]

		if keyNode.Line > lastLine {
			lastLine = keyNode.Line
		}

		if valueNode.Line > lastLine {
			lastLine = valueNode.Line
		}

		var newValue string

		switch keyNode.Value {
		case "title":
			newValue = link.Title
		case "url":
			newValue = link.URL
		case "description":
			descriptionPresent = true
			newValue = link.Description
		default:
			continue
		}

		if newValue == valueNode.Value {
			continue
		}

		if valueNode.Kind != yaml.ScalarNode || valueNode.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 || valueNode.Line != keyNode.Line {
			return nil, fmt.Errorf("cannot edit bookmark: %s must be a single-line value", keyNode.Value)
		}

		if keyNode.Line < 1 || keyNode.Line > len(lines) || keyNode.Column < 1 || keyNode.Column > len(lines[keyNode.Line-1]) {
			return nil, errors.New("cannot edit bookmark: the bookmark position is out of range")
		}

		if keyNode.Value == "description" && newValue == "" {
			if keyNode.Line == linkNode.Line {
				return nil, errors.New("cannot edit bookmark: description must be on its own line")
			}

			descriptionLine = keyNode.Line - 1
			continue
		}

		encoded, err := marshalYAMLString(newValue)
		if err != nil {
			return nil, err
		}

		suffix := ""
		if valueNode.LineComment != "" {
			suffix = " " + valueNode.LineComment
		}

		line := lines[keyNode.Line-1]
		replaced[keyNode.Line-1] = line[:keyNode.Column-1] + keyNode.Value + ": " + encoded + suffix
	}

	for lineIndex, text := range replaced {
		lines[lineIndex] = text
	}

	switch {
	case descriptionLine != -1:
		lines = append(lines[:descriptionLine], lines[descriptionLine+1:]...)
	case link.Description != "" && !descriptionPresent:
		if lastLine < 1 || lastLine > len(lines) {
			return nil, errors.New("cannot edit bookmark: the bookmark position is out of range")
		}

		encoded, err := marshalYAMLString(link.Description)
		if err != nil {
			return nil, err
		}

		newLine := strings.Repeat(" ", linkNode.Column-1) + "description: " + encoded
		lines = append(lines[:lastLine], append([]string{newLine}, lines[lastLine:]...)...)
	}

	return []byte(strings.Join(lines, "\n")), nil
}

// removeBookmarkLinkFromFile deletes the lines that make up the bookmark.
func removeBookmarkLinkFromFile(contents []byte, linkNode *yaml.Node) ([]byte, error) {
	if linkNode.Kind != yaml.MappingNode || linkNode.Style&yaml.FlowStyle != 0 {
		return nil, errors.New("cannot delete bookmark: the bookmark must use block style")
	}

	if linkNode.Line < 1 {
		return nil, errors.New("cannot delete bookmark: the bookmark position is out of range")
	}

	lines := strings.Split(string(contents), "\n")
	start := linkNode.Line - 1

	if start >= len(lines) {
		return nil, errors.New("cannot delete bookmark: the bookmark position is out of range")
	}

	// The bookmark can also be written with the dash on its own line.
	if start > 0 && strings.TrimSpace(lines[start-1]) == "-" {
		start--
	}

	end := start

	var walk func(node *yaml.Node)
	walk = func(node *yaml.Node) {
		if node.Line-1 > end {
			end = node.Line - 1
		}

		for _, child := range node.Content {
			walk(child)
		}
	}
	walk(linkNode)

	if end >= len(lines) {
		end = len(lines) - 1
	}

	result := make([]string, 0, len(lines))
	result = append(result, lines[:start]...)
	result = append(result, lines[end+1:]...)

	// Removing a bookmark can leave the blank lines that separated it from its
	// neighbors next to each other, so drop one of them.
	if start > 0 && start < len(result) && strings.TrimSpace(result[start-1]) == "" && strings.TrimSpace(result[start]) == "" {
		result = append(result[:start], result[start+1:]...)
	}

	return []byte(strings.Join(result, "\n")), nil
}

func marshalYAMLString(value string) (string, error) {
	encoded, err := yaml.Marshal(value)
	if err != nil {
		return "", err
	}

	return strings.TrimRight(string(encoded), "\n"), nil
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

// bookmarkRequestPayload is the body of the requests that create or update a
// bookmark.
type bookmarkRequestPayload struct {
	GroupIndex  int    `json:"group-index"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
}

// handleRequest handles the bookmark endpoints:
//
//	POST   /api/widgets/{widget}/bookmarks
//	PUT    /api/widgets/{widget}/bookmarks/{group}/{bookmark}
//	DELETE /api/widgets/{widget}/bookmarks/{group}/{bookmark}
func (widget *bookmarksWidget) handleRequest(w http.ResponseWriter, r *http.Request) {
	segments := strings.Split(strings.Trim(r.PathValue("path"), "/"), "/")

	if segments[0] != "bookmarks" {
		http.NotFound(w, r)
		return
	}

	if !widget.AllowAdd {
		http.Error(w, "editing bookmarks is not enabled for this widget", http.StatusForbidden)
		return
	}

	switch len(segments) {
	case 1:
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}

		widget.handleAddBookmark(w, r)
	case 3:
		groupIndex, groupErr := strconv.Atoi(segments[1])
		linkIndex, linkErr := strconv.Atoi(segments[2])

		if groupErr != nil || linkErr != nil {
			http.NotFound(w, r)
			return
		}

		switch r.Method {
		case http.MethodPut:
			widget.handleUpdateBookmark(w, r, groupIndex, linkIndex)
		case http.MethodDelete:
			widget.handleDeleteBookmark(w, groupIndex, linkIndex)
		default:
			methodNotAllowed(w, http.MethodPut+", "+http.MethodDelete)
		}
	default:
		http.NotFound(w, r)
	}
}

func methodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

func readBookmarkPayload(w http.ResponseWriter, r *http.Request) (bookmarkRequestPayload, bool) {
	var payload bookmarkRequestPayload

	body := http.MaxBytesReader(w, r.Body, bookmarksMaxRequestBodySize)
	if err := json.NewDecoder(body).Decode(&payload); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return payload, false
	}

	payload.Title = strings.TrimSpace(payload.Title)
	payload.URL = strings.TrimSpace(payload.URL)
	payload.Description = strings.TrimSpace(payload.Description)

	if err := payload.validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return payload, false
	}

	return payload, true
}

// validate returns a message describing the first problem with the payload.
func (payload bookmarkRequestPayload) validate() error {
	switch {
	case payload.Title == "":
		return errors.New("title is required")
	case len([]rune(payload.Title)) > bookmarksMaxTitleLength:
		return fmt.Errorf("title is longer than %d characters", bookmarksMaxTitleLength)
	case len([]rune(payload.Description)) > bookmarksMaxDescriptionLength:
		return fmt.Errorf("description is longer than %d characters", bookmarksMaxDescriptionLength)
	case len(payload.URL) > bookmarksMaxURLLength:
		return fmt.Errorf("url is longer than %d characters", bookmarksMaxURLLength)
	}

	parsedURL, err := url.Parse(payload.URL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" {
		return errors.New("url must be a valid http or https URL")
	}

	return nil
}

func renderBookmarkCard(w http.ResponseWriter, link bookmarkLink) (string, bool) {
	cardHTML, err := executeTemplateToString(bookmarkCardTemplate, link)
	if err != nil {
		slog.Error("Failed to render bookmark card", "error", err)
		http.Error(w, "failed to render bookmark", http.StatusInternalServerError)
		return "", false
	}

	return cardHTML, true
}

func writeHTML(w http.ResponseWriter, html string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(html))
}

// bookmarkGroupAtIndex returns the group with the given index.
func (widget *bookmarksWidget) bookmarkGroupAtIndex(groupIndex int) (*bookmarkGroup, error) {
	if groupIndex < 0 || groupIndex >= len(widget.Groups) {
		return nil, errBookmarkGroupNotFound
	}

	return &widget.Groups[groupIndex], nil
}

// bookmarkAtIndex returns the group and the link with the given indexes.
func (widget *bookmarksWidget) bookmarkAtIndex(groupIndex, linkIndex int) (*bookmarkGroup, *bookmarkLink, error) {
	group, err := widget.bookmarkGroupAtIndex(groupIndex)
	if err != nil {
		return nil, nil, err
	}

	if linkIndex < 0 || linkIndex >= len(group.Links) {
		return nil, nil, errBookmarkNotFound
	}

	return group, &group.Links[linkIndex], nil
}

// writeBookmarkLookupError responds to a request that refers to a group or
// bookmark that does not exist.
func writeBookmarkLookupError(w http.ResponseWriter, err error) {
	if errors.Is(err, errBookmarkNotFound) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	http.Error(w, err.Error(), http.StatusBadRequest)
}

// writeBookmarkSaveError responds to a request whose change could not be
// written to the config file.
func (widget *bookmarksWidget) writeBookmarkSaveError(w http.ResponseWriter, action string, err error) {
	slog.Error("Failed to "+action+" the bookmark in the config file", "error", err, "config", widget.configPath)
	http.Error(w, "failed to "+action+" bookmark: "+err.Error(), http.StatusInternalServerError)
}

func (widget *bookmarksWidget) handleAddBookmark(w http.ResponseWriter, r *http.Request) {
	payload, ok := readBookmarkPayload(w, r)
	if !ok {
		return
	}

	group, err := widget.bookmarkGroupAtIndex(payload.GroupIndex)
	if err != nil {
		writeBookmarkLookupError(w, err)
		return
	}

	link := group.newLink(payload)

	cardHTML, ok := renderBookmarkCard(w, link)
	if !ok {
		return
	}

	if err := widget.appendBookmarkToConfig(payload.GroupIndex, link); err != nil {
		widget.writeBookmarkSaveError(w, "save", err)
		return
	}

	group.Links = append(group.Links, link)
	widget.rerender()

	writeHTML(w, cardHTML)
}

func (widget *bookmarksWidget) handleUpdateBookmark(w http.ResponseWriter, r *http.Request, groupIndex, linkIndex int) {
	payload, ok := readBookmarkPayload(w, r)
	if !ok {
		return
	}

	_, link, err := widget.bookmarkAtIndex(groupIndex, linkIndex)
	if err != nil {
		writeBookmarkLookupError(w, err)
		return
	}

	// Keep the link's other options (icon, target, ...) which stay in the config.
	updated := *link
	updated.Title = payload.Title
	updated.URL = payload.URL
	updated.Description = payload.Description

	cardHTML, ok := renderBookmarkCard(w, updated)
	if !ok {
		return
	}

	if err := widget.updateBookmarkInConfig(groupIndex, linkIndex, updated); err != nil {
		widget.writeBookmarkSaveError(w, "save", err)
		return
	}

	*link = updated
	widget.rerender()

	writeHTML(w, cardHTML)
}

func (widget *bookmarksWidget) handleDeleteBookmark(w http.ResponseWriter, groupIndex, linkIndex int) {
	group, _, err := widget.bookmarkAtIndex(groupIndex, linkIndex)
	if err != nil {
		writeBookmarkLookupError(w, err)
		return
	}

	if err := widget.deleteBookmarkFromConfig(groupIndex, linkIndex); err != nil {
		widget.writeBookmarkSaveError(w, "delete", err)
		return
	}

	group.Links = append(group.Links[:linkIndex], group.Links[linkIndex+1:]...)
	widget.rerender()

	w.WriteHeader(http.StatusNoContent)
}
