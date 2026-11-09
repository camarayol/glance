// Run with: node --test tests/*.test.mjs
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import vm from "node:vm";
import { setupBookmarkSearch } from "../internal/glance/static/js/bookmark-search.js";

const pageSource = readFileSync(new URL("../internal/glance/static/js/page.js", import.meta.url), "utf8");
const originalSearch = pageSource.slice(
    pageSource.indexOf("function setupSearchBoxes() {"),
    pageSource.indexOf("function setupDynamicRelativeTime() {"),
);

const defaultBangs = [
    { shortcut: "!gg", title: "Google", url: "https://google.test/?q=!QUERY!" },
    { shortcut: "!bm", title: "Bookmarks", url: "bookmarks" },
];

function setup(t, bangs = defaultBangs, { withBookmark = false, grouped = false, bookmarkEntries } = {}) {
    let document;
    class Target {
        listeners = new Map();
        addEventListener(type, callback) {
            if (!this.listeners.has(type)) this.listeners.set(type, new Set());
            this.listeners.get(type).add(callback);
        }
        removeEventListener(type, callback) {
            this.listeners.get(type)?.delete(callback);
        }
        dispatchEvent(event) {
            Object.defineProperty(event, "target", { value: this, configurable: true });
            // Listeners registered during dispatch do not run for the current event.
            for (const listener of [...(this.listeners.get(event.type) || [])]) listener(event);
            if (this !== document && event.bubbles && !event.cancelBubble) {
                for (const listener of [...(document.listeners.get(event.type) || [])]) listener(event);
            }
            return !event.defaultPrevented;
        }
    }
    const classList = (...initial) => {
        const values = new Set(initial);
        return {
            add: (name) => values.add(name),
            remove: (name) => values.delete(name),
            contains: (name) => values.has(name),
        };
    };
    const contains = function (element) {
        for (let node = element; node; node = node.parentElement) {
            if (node === this) return true;
        }
        return false;
    };
    const groupContents = { parentElement: null, matches: () => false, contains };
    const panels = new Map();
    const panel = (tab, current = false) => {
        if (!panels.has(tab)) panels.set(tab, {
            parentElement: groupContents,
            classList: classList(...(current ? ["widget-group-content-current"] : [])),
            matches: (selector) => selector === ".widget-group-content",
            getAttribute: (name) => name === "aria-labelledby" ? tab : null,
            contains,
        });
        return panels.get(tab);
    };
    const searchPanel = grouped ? panel("search-tab", true) : null;
    const bookmarkPanel = grouped ? panel("bookmark-tab") : null;
    const input = new Target();
    input.value = "";
    input.tagName = "INPUT";
    input.isConnected = true;
    input.parentElement = searchPanel;
    input.focus = () => {
        if (document.activeElement === input) return;
        if (searchPanel && !searchPanel.classList.contains("widget-group-content-current")) return;
        document.activeElement = input;
        input.dispatchEvent(new Event("focus"));
    };
    input.blur = () => {
        document.activeElement = document.body;
        input.dispatchEvent(new Event("blur"));
    };
    const clickedBookmarks = [];
    const bookmarks = (bookmarkEntries ?? (withBookmark ? [{ title: "GitHub", url: "https://github.com/" }] : [])).map((entry) => {
        const bookmark = new Target();
        bookmark.tagName = "A";
        bookmark.textContent = entry.title;
        const parentPanel = entry.tab ? panel(entry.tab, entry.current) : bookmarkPanel;
        const item = entry.style === "list" ? {
            parentElement: parentPanel, textContent: entry.title, classList: classList(),
            matches: () => false,
        } : null;
        bookmark.parentElement = item || parentPanel;
        bookmark.classList = classList();
        bookmark.matches = (selector) => selector === (item ? ".bookmarks-link" : ".bookmarks-card");
        bookmark.closest = (selector) => selector === "li" ? item : null;
        bookmark.getAttribute = (name) => name === "href" ? entry.url : null;
        bookmark.scrollIntoView = () => {};
        bookmark.click = () => clickedBookmarks.push(bookmark);
        bookmark.focus = () => {
            if (document.activeElement === input) input.blur();
            document.activeElement = bookmark;
        };
        return bookmark;
    });
    const bookmark = bookmarks[0];
    const label = { textContent: "" };
    const kbd = new Target();
    const widget = {
        dataset: { defaultSearchUrl: "https://search.test/?q=!QUERY!", newTab: "true" },
        getElementsByClassName: (name) => [name === "search-input" ? input : label],
        getElementsByTagName: () => [kbd],
        querySelector: () => label,
        querySelectorAll: () => bangs.map((dataset) => ({ dataset })),
    };
    input.closest = () => widget;
    document = new Target();
    document.body = new Target();
    document.body.tagName = "BODY";
    document.activeElement = document.body;
    const classes = new Set();
    document.documentElement = { classList: { add: (name) => classes.add(name), remove: (name) => classes.delete(name) } };
    document.getElementsByClassName = () => [widget];
    document.getElementById = (id) => {
        const selected = panels.get(id);
        if (!selected) return null;
        return { click() {
            for (const current of panels.values()) current.classList.remove("widget-group-content-current");
            selected.classList.add("widget-group-content-current");
            if (searchPanel && selected !== searchPanel && document.activeElement === input) input.blur();
        } };
    };
    document.querySelectorAll = (selector) => {
        if (selector === ".search .search-input") return [input];
        if (selector === ".widget-type-bookmarks a.bookmarks-card, .widget-type-bookmarks a.bookmarks-link") return bookmarks;
        return [];
    };
    const opened = [];
    const window = {
        open: (url, target) => { opened.push({ url, target }); return { focus() {} }; },
        location: {},
        matchMedia: () => ({ matches: true }),
    };
    const previousDocument = globalThis.document;
    const previousObserver = globalThis.MutationObserver;
    const previousWindow = globalThis.window;
    globalThis.document = document;
    globalThis.MutationObserver = class { observe() {} };
    globalThis.window = window;
    t.after(() => {
        if (previousDocument === undefined) delete globalThis.document;
        else globalThis.document = previousDocument;
        if (previousObserver === undefined) delete globalThis.MutationObserver;
        else globalThis.MutationObserver = previousObserver;
        if (previousWindow === undefined) delete globalThis.window;
        else globalThis.window = previousWindow;
    });
    vm.runInNewContext(`${originalSearch}\nsetupSearchBoxes();`, {
        document, window, requestAnimationFrame: (callback) => callback(),
    });
    setupBookmarkSearch();
    input.focus();
    const type = (value) => {
        input.value = value;
        input.dispatchEvent(new Event("input", { bubbles: true }));
    };
    const key = (key, extra = {}) => {
        const event = new Event("keydown", { bubbles: true, cancelable: true });
        Object.assign(event, { key, ...extra });
        document.activeElement.dispatchEvent(event);
        return event;
    };
    return { input, label, type, key, opened, window, classes, document, bookmark, bookmarks, clickedBookmarks, panels, searchPanel, bookmarkPanel };
}

test("ordinary web search, query recall and other bangs still use the original handlers", (t) => {
    const { input, label, type, key, opened, window } = setup(t);
    type("hello world");
    key("Enter");
    assert.deepEqual(opened, [{ url: "https://search.test/?q=hello%20world", target: "_blank" }]);
    key("ArrowUp");
    assert.equal(input.value, "hello world");
    type("!gg rust");
    assert.equal(label.textContent, "Google");
    key("Enter", { ctrlKey: true });
    assert.equal(window.location.href, "https://google.test/?q=rust");
    assert.equal(label.textContent, "");
});

test("bookmark mode blocks web submission and does not open bookmarks during IME composition", (t) => {
    const { input, label, type, key, opened, bookmark, clickedBookmarks } = setup(t, defaultBangs, { withBookmark: true });
    type("!bm github");
    assert.equal(label.textContent, "Bookmarks");
    key("Enter", { isComposing: true });
    key("Enter", { keyCode: 229 });
    assert.deepEqual(clickedBookmarks, []);
    assert.equal(key("Enter").defaultPrevented, true);
    assert.deepEqual(clickedBookmarks, [bookmark]);
    assert.equal(input.value, "!bm github");
    assert.deepEqual(opened, []);
});

test("leaving bookmark mode restores ordinary bang handling", (t) => {
    const { label, type, key, opened } = setup(t);
    type("!gg previous");
    type("!bm github");
    type("!gg next");
    assert.equal(label.textContent, "Google");
    key("Enter");
    assert.equal(opened[0].url, "https://google.test/?q=next");
});

test("custom bookmark shortcuts use the configured title and query throughout their lifecycle", (t) => {
    const { input, label, type, key, opened, classes } = setup(t, [
        { shortcut: "b", title: "我的书签", url: "bookmarks" },
    ]);
    type("b GitHub");
    assert.equal(label.textContent, "我的书签");
    assert.equal(classes.has("bookmark-search-filtering"), true);
    assert.equal(key("Enter").defaultPrevented, true);
    assert.deepEqual(opened, []);
    key("Escape");
    assert.equal(input.value, "");
    assert.equal(label.textContent, "");
    assert.equal(classes.has("bookmark-search-filtering"), false);
    type("ordinary");
    key("Enter");
    assert.equal(opened[0].url, "https://search.test/?q=ordinary");
});

test("multiple local bangs are supported and titles remain optional", (t) => {
    const { label, type, key, opened } = setup(t, [
        { shortcut: "!one", title: "Saved links", url: "bookmarks" },
        { shortcut: "!two", url: "bookmarks" },
    ]);
    type("!one github");
    assert.equal(label.textContent, "Saved links");
    type("!two rust");
    assert.equal(label.textContent, "");
    assert.equal(key("Enter").defaultPrevented, true);
    assert.deepEqual(opened, []);
});

test("without a local bang !bm remains an ordinary web query", (t) => {
    const { type, key, opened } = setup(t, []);
    type("!bm github");
    key("Enter");
    assert.equal(opened[0].url, "https://search.test/?q=!bm%20github");
});

test("a normal URL configured for !bm is not intercepted even when another local bang exists", (t) => {
    const { label, type, key, opened } = setup(t, [
        { shortcut: "!bm", title: "External", url: "https://example.test/?q=!QUERY!" },
        { shortcut: "!b", title: "Local", url: "bookmarks" },
    ]);
    type("!bm github");
    assert.equal(label.textContent, "External");
    key("Enter");
    assert.equal(opened[0].url, "https://example.test/?q=github");
});

test("duplicate shortcuts follow the original last-entry-wins behavior", (t) => {
    const { label, type, key, opened } = setup(t, [
        { shortcut: "!bm", title: "Local", url: "bookmarks" },
        { shortcut: "!bm", title: "External", url: "https://example.test/?q=!QUERY!" },
        { shortcut: "!b", title: "Local", url: "bookmarks" },
    ]);
    type("!bm github");
    assert.equal(label.textContent, "External");
    key("Enter");
    assert.equal(opened[0].url, "https://example.test/?q=github");
});

test("bookmark shortcuts are case-sensitive like other bangs", (t) => {
    const { type, key, opened } = setup(t);
    type("!BM github");
    key("Enter");
    assert.equal(opened[0].url, "https://search.test/?q=!BM%20github");
});

test("a local bang without a query neither filters nor opens a URL", (t) => {
    const { label, type, key, opened, classes, clickedBookmarks } = setup(t, defaultBangs, { withBookmark: true });
    type("!bm");
    assert.equal(label.textContent, "Bookmarks");
    assert.equal(classes.has("bookmark-search-filtering"), false);
    key("Enter");
    assert.deepEqual(opened, []);
    assert.deepEqual(clickedBookmarks, []);
});

test("typing a bookmark query does not switch Group tabs or blur the search input", (t) => {
    const { input, type, document, bookmark, searchPanel, bookmarkPanel } = setup(t, defaultBangs, {
        withBookmark: true, grouped: true,
    });
    type("!bm g");
    assert.equal(bookmark.classList.contains("bookmark-search-target"), true);
    assert.equal(searchPanel.classList.contains("widget-group-content-current"), true);
    assert.equal(bookmarkPanel.classList.contains("widget-group-content-current"), false);
    assert.equal(document.activeElement, input);
    type("!bm github");
    assert.equal(document.activeElement, input);
});

test("Enter directly opens a match in another Group tab without hiding the search input", (t) => {
    const { input, type, key, document, bookmark, clickedBookmarks, searchPanel } = setup(t, defaultBangs, {
        withBookmark: true, grouped: true,
    });
    type("!bm github");
    assert.equal(key("Enter").defaultPrevented, true);
    assert.deepEqual(clickedBookmarks, [bookmark]);
    assert.equal(document.activeElement, input);
    assert.equal(searchPanel.classList.contains("widget-group-content-current"), true);
});

test("Escape from a bookmark restores the search tab and focus", (t) => {
    const { input, label, type, key, document, bookmark, searchPanel, bookmarkPanel, opened, classes } = setup(t, defaultBangs, {
        withBookmark: true, grouped: true,
    });
    type("!bm github");
    document.getElementById("bookmark-tab").click();
    bookmark.focus();
    key("Escape");
    assert.equal(searchPanel.classList.contains("widget-group-content-current"), true);
    assert.equal(bookmarkPanel.classList.contains("widget-group-content-current"), false);
    assert.equal(document.activeElement, input);
    assert.equal(input.value, "");
    assert.equal(label.textContent, "");
    assert.equal(classes.has("bookmark-search-filtering"), false);
    assert.deepEqual(opened, []);
});

test("Escape from a focused bookmark clears the label and stale web bang before empty Enter", (t) => {
    const { input, label, type, key, document, bookmark, opened } = setup(t, defaultBangs, { withBookmark: true });
    type("!gg previous");
    type("!bm github");
    bookmark.focus();
    assert.equal(document.activeElement, bookmark);
    key("Escape");
    assert.equal(document.activeElement, input);
    assert.equal(input.value, "");
    assert.equal(label.textContent, "");
    key("Enter");
    assert.deepEqual(opened, []);
    type("plain query");
    key("Enter");
    assert.equal(opened[0].url, "https://search.test/?q=plain%20query");
});

test("all bookmark widgets in a Group are searched, including list links in inactive tabs", (t) => {
    const { input, type, key, document, bookmarks, clickedBookmarks, panels, opened } = setup(t, defaultBangs, {
        bookmarkEntries: [
            { title: "GitHub", url: "https://github.com/", tab: "code-tab", current: true },
            { title: "Rust docs", url: "https://doc.rust-lang.org/", tab: "docs-tab", style: "list" },
        ],
    });
    assert.equal(panels.get("docs-tab").classList.contains("widget-group-content-current"), false);
    type("!bm rust");
    assert.equal(bookmarks[0].classList.contains("bookmark-search-hidden"), true);
    assert.equal(bookmarks[1].classList.contains("bookmark-search-target"), true);
    assert.equal(bookmarks[1].closest("li").classList.contains("bookmark-search-hidden"), false);
    assert.equal(panels.get("docs-tab").classList.contains("widget-group-content-current"), true);
    assert.equal(document.activeElement, input);
    key("Enter");
    assert.deepEqual(clickedBookmarks, [bookmarks[1]]);
    assert.deepEqual(opened, []);
});

test("arrows and Tab cycle matches across Group widgets and standalone widgets before Enter opens them", (t) => {
    const { input, type, key, document, bookmarks, clickedBookmarks, panels } = setup(t, defaultBangs, {
        bookmarkEntries: [
            { title: "GitHub main", url: "https://github.com/", tab: "main-tab", current: true },
            { title: "GitHub mirror", url: "https://mirror.test/", tab: "mirror-tab" },
            { title: "GitHub status", url: "https://status.test/" },
        ],
    });
    type("!bm github");
    key("ArrowDown");
    assert.equal(bookmarks[1].classList.contains("bookmark-search-target"), true);
    assert.equal(panels.get("mirror-tab").classList.contains("widget-group-content-current"), true);
    key("Enter");
    assert.deepEqual(clickedBookmarks, [bookmarks[1]]);
    key("Tab");
    key("Enter");
    assert.deepEqual(clickedBookmarks, [bookmarks[1], bookmarks[2]]);
    key("Tab", { shiftKey: true });
    key("ArrowUp");
    key("Enter");
    assert.deepEqual(clickedBookmarks, [bookmarks[1], bookmarks[2], bookmarks[0]]);
    assert.equal(document.activeElement, input);
});

test("Enter with no matching bookmark never opens a bookmark or falls back to a web search", (t) => {
    const { type, key, opened, clickedBookmarks } = setup(t, defaultBangs, { withBookmark: true });
    type("!bm missing");
    assert.equal(key("Enter").defaultPrevented, true);
    assert.deepEqual(clickedBookmarks, []);
    assert.deepEqual(opened, []);
});

test("Escape resets bookmark mode and clears the original bang state", (t) => {
    const { input, label, type, key, opened } = setup(t);
    type("!gg previous");
    type("!bm github");
    key("Escape");
    assert.equal(input.value, "");
    assert.equal(label.textContent, "");
    type("plain query");
    key("Enter");
    assert.equal(opened[0].url, "https://search.test/?q=plain%20query");
});
