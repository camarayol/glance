package glance

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

var (
	pageTemplate        = mustParseTemplate("page.html", "document.html", "footer.html")
	pageContentTemplate = mustParseTemplate("page-content.html")
	manifestTemplate    = mustParseTemplate("manifest.json")
)

const STATIC_ASSETS_CACHE_DURATION = 24 * time.Hour

// DEFAULT_WIDGET_UPDATE_INTERVAL is used when server.widget-update-interval
// is not set in the config. It determines how often widgets are checked in the
// background so that data is refreshed proactively, before a page requests it.
const DEFAULT_WIDGET_UPDATE_INTERVAL = 1 * time.Minute

var reservedPageSlugs = []string{"login", "logout"}

type application struct {
	Version   string
	CreatedAt time.Time
	Config    config

	parsedManifest []byte

	slugToPage     map[string]*page
	widgetByID     map[uint64]widget
	widgetPageByID map[uint64]*page

	// bookmarksWidgetByID maps the config-level id of a bookmarks widget to
	// the widget, so that requests can refer to it by a stable name.
	bookmarksWidgetByID map[string]*bookmarksWidget

	RequiresAuth           bool
	authSecretKey          []byte
	usernameHashToUsername map[string]string
	authAttemptsMu         sync.Mutex
	failedAuthAttempts     map[string]*failedAuthAttempt
}

func newApplication(c *config) (*application, error) {
	app := &application{
		Version:             buildVersion,
		CreatedAt:           time.Now(),
		Config:              *c,
		slugToPage:          make(map[string]*page),
		widgetByID:          make(map[uint64]widget),
		widgetPageByID:      make(map[uint64]*page),
		bookmarksWidgetByID: make(map[string]*bookmarksWidget),
	}
	config := &app.Config

	//
	// Init auth
	//

	if len(config.Auth.Users) > 0 {
		secretBytes, err := base64.StdEncoding.DecodeString(config.Auth.SecretKey)
		if err != nil {
			return nil, fmt.Errorf("decoding secret-key: %v", err)
		}

		if len(secretBytes) != AUTH_SECRET_KEY_LENGTH {
			return nil, fmt.Errorf("secret-key must be exactly %d bytes", AUTH_SECRET_KEY_LENGTH)
		}

		app.usernameHashToUsername = make(map[string]string)
		app.failedAuthAttempts = make(map[string]*failedAuthAttempt)
		app.RequiresAuth = true

		for username := range config.Auth.Users {
			user := config.Auth.Users[username]
			usernameHash, err := computeUsernameHash(username, secretBytes)
			if err != nil {
				return nil, fmt.Errorf("computing username hash for user %s: %v", username, err)
			}
			app.usernameHashToUsername[string(usernameHash)] = username

			if user.PasswordHashString != "" {
				user.PasswordHash = []byte(user.PasswordHashString)
				user.PasswordHashString = ""
			} else {
				hashedPassword, err := bcrypt.GenerateFromPassword([]byte(user.Password), bcrypt.DefaultCost)
				if err != nil {
					return nil, fmt.Errorf("hashing password for user %s: %v", username, err)
				}

				user.Password = ""
				user.PasswordHash = hashedPassword
			}
		}

		app.authSecretKey = secretBytes
	}

	//
	// Init themes
	//

	if !config.Theme.DisablePicker {
		themeKeys := make([]string, 0, 2)
		themeProps := make([]*themeProperties, 0, 2)

		defaultDarkTheme, ok := config.Theme.Presets.Get("default-dark")
		if ok && !config.Theme.SameAs(defaultDarkTheme) || !config.Theme.SameAs(&themeProperties{}) {
			themeKeys = append(themeKeys, "default-dark")
			themeProps = append(themeProps, &themeProperties{})
		}

		themeKeys = append(themeKeys, "default-light")
		themeProps = append(themeProps, &themeProperties{
			Light:                    true,
			BackgroundColor:          &hslColorField{240, 13, 95},
			PrimaryColor:             &hslColorField{230, 100, 30},
			NegativeColor:            &hslColorField{0, 70, 50},
			ContrastMultiplier:       1.3,
			TextSaturationMultiplier: 0.5,
		})

		themePresets, err := newOrderedYAMLMap(themeKeys, themeProps)
		if err != nil {
			return nil, fmt.Errorf("creating theme presets: %v", err)
		}
		config.Theme.Presets = *themePresets.Merge(&config.Theme.Presets)

		for key, properties := range config.Theme.Presets.Items() {
			properties.Key = key
			if err := properties.init(); err != nil {
				return nil, fmt.Errorf("initializing preset theme %s: %v", key, err)
			}
		}
	}

	config.Theme.Key = "default"
	if err := config.Theme.init(); err != nil {
		return nil, fmt.Errorf("initializing default theme: %v", err)
	}

	//
	// Init pages
	//

	app.slugToPage[""] = &config.Pages[0]

	providers := &widgetProviders{
		assetResolver: app.StaticAssetPath,
	}

	// Widgets nested inside containers such as groups are registered as well,
	// otherwise they can't be looked up by ID (e.g. for widget requests).
	var registerWidget func(widget widget, page *page)
	registerWidget = func(widget widget, page *page) {
		app.widgetByID[widget.GetID()] = widget
		app.widgetPageByID[widget.GetID()] = page

		if bookmarks, ok := widget.(*bookmarksWidget); ok {
			bookmarks.configPath = config.configPath

			if bookmarks.BookmarksID != "" {
				app.bookmarksWidgetByID[bookmarks.BookmarksID] = bookmarks
			}
		}

		container, ok := widget.(interface{ _childWidgets() widgets })
		if !ok {
			return
		}

		for _, child := range container._childWidgets() {
			registerWidget(child, page)
		}
	}

	for p := range config.Pages {
		page := &config.Pages[p]
		page.PrimaryColumnIndex = -1

		if page.Slug == "" {
			page.Slug = titleToSlug(page.Title)
		}

		if slices.Contains(reservedPageSlugs, page.Slug) {
			return nil, fmt.Errorf("page slug \"%s\" is reserved", page.Slug)
		}

		app.slugToPage[page.Slug] = page

		if page.Width == "default" {
			page.Width = ""
		}

		if page.DesktopNavigationWidth == "" && page.DesktopNavigationWidth != "default" {
			page.DesktopNavigationWidth = page.Width
		}

		for i := range page.HeadWidgets {
			widget := page.HeadWidgets[i]
			registerWidget(widget, page)
			widget.setProviders(providers)
		}

		for c := range page.Columns {
			column := &page.Columns[c]

			if page.PrimaryColumnIndex == -1 && column.Size == "full" {
				page.PrimaryColumnIndex = int8(c)
			}

			for w := range column.Widgets {
				widget := column.Widgets[w]
				registerWidget(widget, page)
				widget.setProviders(providers)
			}
		}
	}

	config.Server.BaseURL = strings.TrimRight(config.Server.BaseURL, "/")
	config.Theme.CustomCSSFile = app.resolveUserDefinedAssetPath(config.Theme.CustomCSSFile)
	config.Branding.LogoURL = app.resolveUserDefinedAssetPath(config.Branding.LogoURL)

	config.Branding.FaviconURL = ternary(
		config.Branding.FaviconURL == "",
		app.StaticAssetPath("favicon.svg"),
		app.resolveUserDefinedAssetPath(config.Branding.FaviconURL),
	)

	config.Branding.FaviconType = ternary(
		strings.HasSuffix(config.Branding.FaviconURL, ".svg"),
		"image/svg+xml",
		"image/png",
	)

	if config.Branding.AppName == "" {
		config.Branding.AppName = "Glance"
	}

	if config.Branding.AppIconURL == "" {
		config.Branding.AppIconURL = app.StaticAssetPath("app-icon.png")
	}

	if config.Branding.AppBackgroundColor == "" {
		config.Branding.AppBackgroundColor = config.Theme.BackgroundColorAsHex
	}

	manifest, err := executeTemplateToString(manifestTemplate, templateData{App: app})
	if err != nil {
		return nil, fmt.Errorf("parsing manifest.json: %v", err)
	}
	app.parsedManifest = []byte(manifest)

	return app, nil
}

func (p *page) updateOutdatedWidgets(ctx context.Context) {
	now := time.Now()

	var wg sync.WaitGroup

	updateOutdated := func(list widgets) {
		for w := range list {
			widget := list[w]

			if ctx.Err() != nil {
				return
			}

			if !widget.requiresUpdate(&now) {
				continue
			}

			wg.Add(1)
			go func() {
				defer wg.Done()
				widget.update(ctx)
			}()
		}
	}

	updateOutdated(p.HeadWidgets)

	for c := range p.Columns {
		updateOutdated(p.Columns[c].Widgets)
	}

	wg.Wait()
}

// updateOutdatedWidgetsOnAllPages updates outdated widgets on every page in
// parallel. Pages are locked for the duration of the update so that a request
// never renders a widget that is in the middle of being updated.
func (a *application) updateOutdatedWidgetsOnAllPages(ctx context.Context) {
	var wg sync.WaitGroup

	for p := range a.Config.Pages {
		page := &a.Config.Pages[p]

		wg.Add(1)
		go func() {
			defer wg.Done()

			if ctx.Err() != nil {
				return
			}

			page.mu.Lock()
			defer page.mu.Unlock()

			page.updateOutdatedWidgets(ctx)
		}()
	}

	wg.Wait()
}

// keepWidgetsUpToDate preloads all widgets right after startup so that the
// first page load does not have to wait for any network requests. It then
// keeps checking in the background and refreshes widgets as their cache
// expires, which means content is already warm when someone opens a page.
func (a *application) keepWidgetsUpToDate(ctx context.Context) {
	a.updateOutdatedWidgetsOnAllPages(ctx)

	updateInterval := a.Config.widgetUpdateInterval()

	// A zero or negative interval disables the background refresh, in which
	// case widgets are only updated when a page is opened.
	if updateInterval <= 0 {
		return
	}

	ticker := time.NewTicker(updateInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.updateOutdatedWidgetsOnAllPages(ctx)
		}
	}
}

func (a *application) resolveUserDefinedAssetPath(path string) string {
	if strings.HasPrefix(path, "/assets/") {
		return a.Config.Server.BaseURL + path
	}

	return path
}

type templateRequestData struct {
	Theme *themeProperties
}

type templateData struct {
	App     *application
	Page    *page
	Request templateRequestData
}

func (a *application) populateTemplateRequestData(data *templateRequestData, r *http.Request) {
	theme := &a.Config.Theme.themeProperties

	if !a.Config.Theme.DisablePicker {
		selectedTheme, err := r.Cookie("theme")
		if err == nil {
			preset, exists := a.Config.Theme.Presets.Get(selectedTheme.Value)
			if exists {
				theme = preset
			}
		}
	}

	data.Theme = theme
}

func (a *application) handlePageRequest(w http.ResponseWriter, r *http.Request) {
	page, exists := a.slugToPage[r.PathValue("page")]
	if !exists {
		a.handleNotFound(w, r)
		return
	}

	if a.handleUnauthorizedResponse(w, r, redirectToLogin) {
		return
	}

	data := templateData{
		Page: page,
		App:  a,
	}
	a.populateTemplateRequestData(&data.Request, r)

	var responseBytes bytes.Buffer
	err := pageTemplate.Execute(&responseBytes, data)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))
		return
	}

	w.Write(responseBytes.Bytes())
}

func (a *application) handlePageContentRequest(w http.ResponseWriter, r *http.Request) {
	page, exists := a.slugToPage[r.PathValue("page")]
	if !exists {
		a.handleNotFound(w, r)
		return
	}

	if a.handleUnauthorizedResponse(w, r, showUnauthorizedJSON) {
		return
	}

	pageData := templateData{
		Page: page,
	}

	var err error
	var responseBytes bytes.Buffer

	func() {
		page.mu.Lock()
		defer page.mu.Unlock()

		page.updateOutdatedWidgets(r.Context())
		err = pageContentTemplate.Execute(&responseBytes, pageData)
	}()

	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))
		return
	}

	w.Write(responseBytes.Bytes())
}

func (a *application) addressOfRequest(r *http.Request) string {
	remoteAddrWithoutPort := func() string {
		for i := len(r.RemoteAddr) - 1; i >= 0; i-- {
			if r.RemoteAddr[i] == ':' {
				return r.RemoteAddr[:i]
			}
		}

		return r.RemoteAddr
	}

	if !a.Config.Server.Proxied {
		return remoteAddrWithoutPort()
	}

	// This should probably be configurable or look for multiple headers, not just this one
	forwardedFor := r.Header.Get("X-Forwarded-For")
	if forwardedFor == "" {
		return remoteAddrWithoutPort()
	}

	ips := strings.Split(forwardedFor, ",")
	if len(ips) == 0 {
		return remoteAddrWithoutPort()
	}

	// Use the last (rightmost) IP in X-Forwarded-For, as this is the
	// one added by the trusted reverse proxy. The leftmost values can
	// be spoofed by the client and must not be trusted for rate limiting
	// or other security-sensitive operations.
	lastIP := strings.TrimSpace(ips[len(ips)-1])
	if lastIP == "" {
		return remoteAddrWithoutPort()
	}

	return lastIP
}

func (a *application) handleNotFound(w http.ResponseWriter, _ *http.Request) {
	// TODO: add proper not found page
	w.WriteHeader(http.StatusNotFound)
	w.Write([]byte("Page not found"))
}

func (a *application) handleWidgetRequest(w http.ResponseWriter, r *http.Request) {
	if a.handleUnauthorizedResponse(w, r, showUnauthorizedJSON) {
		return
	}

	widget, page, exists := a.findWidgetByPathValue(r.PathValue("widget"))
	if !exists {
		a.handleNotFound(w, r)
		return
	}

	// TODO: this requires a rework of the widget update logic so that rather
	// than locking the entire page we lock individual widgets
	page.mu.Lock()
	defer page.mu.Unlock()

	widget.handleRequest(w, r)
}

// findWidgetByPathValue resolves a widget from the value of the {widget} path
// segment. Either the widget's runtime ID or, for widgets that have one, its
// stable config-level id can be used.
func (a *application) findWidgetByPathValue(value string) (widget, *page, bool) {
	if widgetID, err := strconv.ParseUint(value, 10, 64); err == nil {
		found, exists := a.widgetByID[widgetID]
		if !exists {
			return nil, nil, false
		}

		return found, a.widgetPageByID[widgetID], true
	}

	bookmarks, exists := a.bookmarksWidgetByID[value]
	if !exists {
		return nil, nil, false
	}

	return bookmarks, a.widgetPageByID[bookmarks.GetID()], true
}

func (a *application) StaticAssetPath(asset string) string {
	return a.Config.Server.BaseURL + "/static/" + staticFSHash + "/" + asset
}

func (a *application) VersionedAssetPath(asset string) string {
	return a.Config.Server.BaseURL + "/" + asset +
		"?v=" + strconv.FormatInt(a.CreatedAt.Unix(), 10)
}

func (a *application) server() (func() error, func() error) {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /{$}", a.handlePageRequest)
	mux.HandleFunc("GET /{page}", a.handlePageRequest)

	mux.HandleFunc("GET /api/pages/{page}/content/{$}", a.handlePageContentRequest)

	if !a.Config.Theme.DisablePicker {
		mux.HandleFunc("POST /api/set-theme/{key}", a.handleThemeChangeRequest)
	}

	mux.HandleFunc("POST /api/widgets/{widget}/{path...}", a.handleWidgetRequest)
	mux.HandleFunc("PUT /api/widgets/{widget}/{path...}", a.handleWidgetRequest)
	mux.HandleFunc("DELETE /api/widgets/{widget}/{path...}", a.handleWidgetRequest)
	mux.HandleFunc("GET /api/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	if a.RequiresAuth {
		mux.HandleFunc("GET /login", a.handleLoginPageRequest)
		mux.HandleFunc("GET /logout", a.handleLogoutRequest)
		mux.HandleFunc("POST /api/authenticate", a.handleAuthenticationAttempt)
	}

	mux.Handle(
		fmt.Sprintf("GET /static/%s/{path...}", staticFSHash),
		http.StripPrefix(
			"/static/"+staticFSHash,
			fileServerWithCache(http.FS(staticFS), STATIC_ASSETS_CACHE_DURATION),
		),
	)

	assetCacheControlValue := fmt.Sprintf(
		"public, max-age=%d",
		int(STATIC_ASSETS_CACHE_DURATION.Seconds()),
	)

	mux.HandleFunc(fmt.Sprintf("GET /static/%s/css/bundle.css", staticFSHash), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Cache-Control", assetCacheControlValue)
		w.Header().Add("Content-Type", "text/css; charset=utf-8")
		w.Write(bundledCSSContents)
	})

	mux.HandleFunc("GET /manifest.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Cache-Control", assetCacheControlValue)
		w.Header().Add("Content-Type", "application/json")
		w.Write(a.parsedManifest)
	})

	updateCtx, stopWidgetUpdates := context.WithCancel(context.Background())
	go a.keepWidgetsUpToDate(updateCtx)

	var absAssetsPath string
	if a.Config.Server.AssetsPath != "" {
		absAssetsPath, _ = filepath.Abs(a.Config.Server.AssetsPath)
		assetsFS := fileServerWithCache(http.Dir(a.Config.Server.AssetsPath), 2*time.Hour)
		mux.Handle("/assets/{path...}", http.StripPrefix("/assets/", assetsFS))
	}

	server := http.Server{
		Addr:              fmt.Sprintf("%s:%d", a.Config.Server.Host, a.Config.Server.Port),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
	}

	start := func() error {
		log.Printf("Starting server on %s:%d (base-url: \"%s\", assets-path: \"%s\")\n",
			a.Config.Server.Host,
			a.Config.Server.Port,
			a.Config.Server.BaseURL,
			absAssetsPath,
		)

		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			return err
		}

		return nil
	}

	stop := func() error {
		stopWidgetUpdates()
		return server.Close()
	}

	return start, stop
}
