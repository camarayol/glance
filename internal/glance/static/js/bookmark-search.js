const bookmarkLinkSelector = ".widget-type-bookmarks a.bookmarks-card, .widget-type-bookmarks a.bookmarks-link";
const hiddenClass = "bookmark-search-hidden";
const targetClass = "bookmark-search-target";

function normalize(value) {
    return value.normalize("NFKC").toLowerCase().replace(/\s+/g, " ").trim();
}

function getBookmarkBang(value, bangs) {
    const shortcut = value.trim().split(/\s+/, 1)[0];
    const bang = bangs?.get(shortcut);
    return bang?.url === "bookmarks" ? bang : null;
}

// null means a regular web search; an empty string means a local bang without a query.
export function parseBookmarkQuery(value, bangs) {
    const bang = getBookmarkBang(value, bangs);
    if (!bang) return null;
    return normalize(value.trim().slice(bang.shortcut.length));
}

export function matchesBookmarkQuery(text, query) {
    text = normalize(text);
    return normalize(query).split(" ").filter(Boolean).every((term) => text.includes(term));
}

function searchableText(link) {
    const item = link.matches(".bookmarks-card") ? link : link.closest("li") || link;
    const groupTitle = link.closest(".bookmarks-group")?.querySelector(".bookmarks-group-title")?.textContent || "";
    const url = link.getAttribute("href") || "";
    let decodedURL = url;
    try { decodedURL = decodeURIComponent(url); } catch { /* Keep malformed escapes literal. */ }
    return `${item.textContent} ${url} ${decodedURL} ${groupTitle}`;
}

function revealGroupTabs(element, keepVisible = null) {
    // Inactive group tabs are still in the DOM. Activate them from outside in.
    const panels = [];
    for (let parent = element.parentElement; parent; parent = parent.parentElement) {
        if (parent.matches(".widget-group-content")) panels.push(parent);
    }
    // Do not switch away from a tab containing the input while it is being edited.
    if (keepVisible && panels.some((panel) => panel.parentElement.contains(keepVisible) && !panel.contains(keepVisible))) return;
    panels.reverse().forEach((panel) => {
        if (!panel.classList.contains("widget-group-content-current")) {
            document.getElementById(panel.getAttribute("aria-labelledby"))?.click();
        }
    });
}

// Extend only bangs with url: bookmarks; ordinary search remains owned by page.js.
export function setupBookmarkSearch() {
    const inputs = Array.from(document.querySelectorAll(".search .search-input"));
    const bangsByInput = new WeakMap();
    for (const input of inputs) {
        const bangs = input.closest(".search").querySelectorAll(".search-bangs > input");
        bangsByInput.set(input, new Map(Array.from(bangs, (bang) => [bang.dataset.shortcut, bang.dataset])));
    }
    if (!inputs.some((input) => Array.from(bangsByInput.get(input).values()).some((bang) => bang.url === "bookmarks"))) return;
    const bookmarks = new BookmarkSearch(bangsByInput);

    for (const input of inputs) {
        const bangs = bangsByInput.get(input);
        const bangLabel = input.closest(".search").querySelector(".search-bang");
        const synchronize = () => {
            const wasActive = bookmarks.input === input;
            bookmarks.update(input);
            const bang = getBookmarkBang(input.value, bangs);
            if (bang) {
                bangLabel.textContent = bang.title || "";
            } else if (wasActive) {
                bangLabel.textContent = "";
            }
        };

        input.addEventListener("input", (event) => {
            // page.js listens on document. Keep local queries out of its bang state.
            if (getBookmarkBang(input.value, bangs)) event.stopPropagation();
            if (!event.isComposing) synchronize();
        });
        input.addEventListener("compositionend", synchronize);
        input.addEventListener("focus", synchronize);
        input.addEventListener("keydown", (event) => {
            if (!getBookmarkBang(input.value, bangs)) return;
            // Stop before the original document handler can submit a web search.
            event.stopPropagation();
            if (event.isComposing || event.keyCode === 229) return;
            bookmarks.handleKeyDown(event, input);
        });
        if (input === document.activeElement) synchronize();
    }
}

export class BookmarkSearch {
    constructor(bangsByInput) {
        this.bangsByInput = bangsByInput;
        this.input = null;
        this.query = null;
        this.matches = [];
        this.index = 0;
        this.target = null;
        this.hiddenElements = new Set();
        let pendingFrame = 0;

        // Keep the filter current when bookmark cards are added, edited or deleted.
        new MutationObserver((records) => {
            if (!this.input || pendingFrame) return;
            const relevant = records.some((record) => {
                const target = record.target instanceof Element ? record.target : record.target.parentElement;
                if (target?.closest(".widget-type-bookmarks")) return true;
                return [...record.addedNodes, ...record.removedNodes].some((node) =>
                    node instanceof Element && (
                        node.matches(".widget-type-bookmarks") || node.querySelector(".widget-type-bookmarks")
                    )
                );
            });
            if (!relevant) return;
            pendingFrame = requestAnimationFrame(() => {
                pendingFrame = 0;
                if (this.input?.isConnected) this.update(this.input);
                else this.clear();
            });
        }).observe(document.getElementById("page-content") || document.body, {
            childList: true,
            subtree: true,
            characterData: true,
            attributes: true,
            attributeFilter: ["href"],
        });

        document.addEventListener("keydown", (event) => {
            if (event.target !== this.target || !this.input?.isConnected || event.isComposing || event.keyCode === 229) return;
            if (event.key === "Escape") {
                event.preventDefault();
                this.reset();
            } else if (event.key === "Tab" && !event.ctrlKey && !event.metaKey && !event.altKey) {
                this.update(this.input);
                if (this.matches.length > 1) {
                    event.preventDefault();
                    this.move(event.shiftKey ? -1 : 1, true);
                }
            }
        });
    }

    clearDecorations() {
        this.hiddenElements.forEach((element) => element.classList.remove(hiddenClass));
        this.hiddenElements.clear();
        document.documentElement.classList.remove("bookmark-search-filtering");
        if (this.target) this.target.classList.remove(targetClass);
        this.target = null;
    }

    clear() {
        this.clearDecorations();
        this.input = null;
        this.query = null;
        this.matches = [];
        this.index = 0;
    }

    hide(element) {
        element.classList.add(hiddenClass);
        this.hiddenElements.add(element);
    }

    update(input) {
        const query = parseBookmarkQuery(input.value, this.bangsByInput.get(input));
        const previous = input === this.input && query === this.query ? this.matches[this.index] : null;
        this.clear();
        if (query === null) return;
        this.input = input;
        this.query = query;
        if (!query) return;

        // Query the entire page, including every bookmark widget in inactive Group tabs.
        const links = Array.from(document.querySelectorAll(bookmarkLinkSelector));
        this.matches = links.filter((link) => matchesBookmarkQuery(searchableText(link), query));
        this.index = Math.max(0, this.matches.indexOf(previous));
        const matched = new Set(this.matches);
        document.documentElement.classList.add("bookmark-search-filtering");
        links.forEach((link) => {
            if (!matched.has(link)) this.hide(link.matches(".bookmarks-card") ? link : link.closest("li") || link);
        });
        document.querySelectorAll(".widget-type-bookmarks .bookmarks-group, .widget-type-bookmarks").forEach((container) => {
            const children = Array.from(container.querySelectorAll("a.bookmarks-card, a.bookmarks-link"));
            if (children.length > 0 && !children.some((link) => matched.has(link))) this.hide(container);
        });
        this.select();
    }

    select(scroll = false, focus = false) {
        if (this.target) this.target.classList.remove(targetClass);
        this.target = this.matches[this.index] || null;
        if (!this.target) return;
        revealGroupTabs(this.target, focus ? null : this.input);
        this.target.classList.add(targetClass);
        if (focus) this.target.focus({ preventScroll: true });
        if (scroll) {
            this.target.scrollIntoView({
                block: "center",
                behavior: window.matchMedia("(prefers-reduced-motion: reduce)").matches ? "instant" : "smooth",
            });
        }
    }

    move(direction, focus = false) {
        if (!this.matches.length) return;
        this.index = (this.index + direction + this.matches.length) % this.matches.length;
        this.select(true, focus);
    }

    reset() {
        const input = this.input;
        if (input?.isConnected) {
            input.value = "";
            revealGroupTabs(input);
            // Restore page.js's focus-scoped listeners before resetting its bang state.
            input.focus({ preventScroll: true });
            input.dispatchEvent(new Event("input", { bubbles: true }));
        }
        this.clear();
    }

    handleKeyDown(event, input) {
        if (event.key === "Escape") {
            event.preventDefault();
            this.update(input);
            this.reset();
            return;
        }
        if (!["Enter", "ArrowDown", "ArrowUp", "Tab"].includes(event.key)) return;
        const isTab = event.key === "Tab";
        if (isTab) {
            if (event.ctrlKey || event.metaKey || event.altKey) return;
        } else if (event.key !== "Enter" && (event.ctrlKey || event.metaKey || event.altKey || event.shiftKey)) {
            return;
        }
        this.update(input);
        if (isTab && this.matches.length < 2) return;
        event.preventDefault();
        if (!this.query) return;
        // Native activation preserves the bookmark's target and rel attributes.
        if (event.key === "Enter") this.target?.click();
        else this.move(isTab ? (event.shiftKey ? -1 : 1) : (event.key === "ArrowDown" ? 1 : -1));
    }
}
