import { elem } from "./templating.js";

let activePopover = null;
let activeMenu = null;
let listenerAttached = false;

export function setup() {
    const controls = document.getElementsByClassName("bookmarks-add");

    for (let i = 0; i < controls.length; i++) {
        decorateControl(controls[i]);
    }

    if (listenerAttached) return;

    listenerAttached = true;
    document.addEventListener("click", onDocumentClick);
    document.addEventListener("contextmenu", onDocumentContextMenu);
}

function decorateControl(control) {
    if (control.dataset.initialized === "true") return;

    control.dataset.initialized = "true";

    const toggle = elem("button")
        .classes("bookmarks-add-toggle")
        .attrs({
            type: "button",
            "aria-label": "Add bookmark",
            "aria-haspopup": "dialog",
            "aria-expanded": "false",
        })
        .append(
            elem("span").classes("bookmarks-add-plus").attrs({ "aria-hidden": "true" }),
            elem("span").classes("bookmarks-add-label").text("Add bookmark")
        );

    control.append(toggle);
}

// Click handling is delegated so that controls added after the initial setup
// (for example after a widget update) keep working, and so that clicks
// anywhere on the card - not just on the label inside it - are registered.
function onDocumentClick(event) {
    if (event.button !== 0) return;
    if (!(event.target instanceof Element)) return;

    const control = event.target.closest(".bookmarks-add");
    if (control === null) return;

    closeActiveMenu();

    if (activePopover !== null && activePopover.anchor === control) {
        closeActivePopover();
        return;
    }

    decorateControl(control);
    openAddEditor(control);
}

// Right clicking a card opens a small menu that allows editing or deleting the
// bookmark in place.
function onDocumentContextMenu(event) {
    if (!(event.target instanceof Element)) return;

    const card = event.target.closest("a.bookmarks-card");
    const location = card === null ? null : cardLocation(card);

    if (location === null) {
        closeActiveMenu();
        return;
    }

    event.preventDefault();
    closeActivePopover();
    closeActiveMenu();

    openContextMenu(card, location, event.clientX, event.clientY);
}

function gridLocation(element) {
    const grid = element.closest(".bookmarks-cards");

    if (grid === null) return null;

    const bookmarksID = grid.dataset.bookmarksId;
    const groupIndex = parseInt(grid.dataset.groupIndex, 10);

    if (!bookmarksID || Number.isNaN(groupIndex)) return null;

    return { grid: grid, bookmarksID: bookmarksID, groupIndex: groupIndex };
}

function cardLocation(card) {
    const location = gridLocation(card);
    if (location === null) return null;

    const cards = Array.from(location.grid.children).filter((child) => {
        return child.classList.contains("bookmarks-card") && !child.classList.contains("bookmarks-add");
    });

    const linkIndex = cards.indexOf(card);
    if (linkIndex === -1) return null;

    location.linkIndex = linkIndex;
    return location;
}

function openContextMenu(card, location, clientX, clientY) {
    const menu = elem("div")
        .classes("bookmarks-menu")
        .attrs({ role: "menu" });

    const reposition = () => positionMenu(menu, clientX, clientY);

    menu.append(
        menuItem("Edit", () => {
            closeActiveMenu();
            openEditEditor(card, location);
        }),
        menuItem("Delete", () => {
            showDeleteConfirmation(menu, card, location, reposition);
        }, "bookmarks-menu-danger")
    );

    document.body.append(menu);
    reposition();

    const onDocumentMouseDown = (event) => {
        if (menu.contains(event.target)) return;

        closeActiveMenu();
    };

    const onDocumentKeyDown = (event) => {
        if (event.key === "Escape") {
            event.preventDefault();
            closeActiveMenu();
            return;
        }

        if (event.key !== "Tab") return;

        const focusables = Array.from(menu.querySelectorAll(".bookmarks-menu-item")).filter((element) => {
            return !element.disabled;
        });

        const index = focusables.indexOf(document.activeElement);
        if (index === -1) return;

        const step = event.shiftKey ? -1 : 1;

        event.preventDefault();
        focusables[(index + step + focusables.length) % focusables.length].focus();
    };

    const onViewportChange = () => closeActiveMenu();

    document.addEventListener("mousedown", onDocumentMouseDown, true);
    document.addEventListener("keydown", onDocumentKeyDown, true);
    window.addEventListener("resize", onViewportChange);
    window.addEventListener("scroll", onViewportChange, true);

    activeMenu = {
        element: menu,
        onDocumentMouseDown: onDocumentMouseDown,
        onDocumentKeyDown: onDocumentKeyDown,
        onViewportChange: onViewportChange,
    };

    menu.querySelector("button")?.focus({ preventScroll: true });
}

function closeActiveMenu() {
    if (activeMenu === null) return;

    const menu = activeMenu;
    activeMenu = null;

    document.removeEventListener("mousedown", menu.onDocumentMouseDown, true);
    document.removeEventListener("keydown", menu.onDocumentKeyDown, true);
    window.removeEventListener("resize", menu.onViewportChange);
    window.removeEventListener("scroll", menu.onViewportChange, true);

    menu.element.remove();
}

function menuItem(label, onClick, extraClass) {
    const button = elem("button")
        .classes("bookmarks-menu-item")
        .attrs({ type: "button", role: "menuitem" })
        .text(label)
        .on("click", onClick);

    if (extraClass !== undefined) {
        button.classes(extraClass);
    }

    return button;
}

function showDeleteConfirmation(menu, card, location, reposition) {
    const error = elem("span").classes("bookmarks-menu-error").attrs({ role: "alert" });

    const confirm = elem("button")
        .classes("bookmarks-menu-item", "bookmarks-menu-danger")
        .attrs({ type: "button" })
        .text("Delete");

    const cancel = elem("button")
        .classes("bookmarks-menu-item")
        .attrs({ type: "button" })
        .text("Cancel")
        .on("click", () => closeActiveMenu());

    confirm.on("click", async () => {
        confirm.disabled = true;
        cancel.disabled = true;
        error.textContent = "";

        try {
            const response = await fetch(bookmarkItemEndpoint(location), { method: "DELETE" });

            if (!response.ok) {
                error.textContent = (await response.text()).trim() || `Failed to delete bookmark (${response.status})`;
                reposition();
                return;
            }

            card.remove();
            closeActiveMenu();
        } catch (err) {
            error.textContent = "Failed to delete bookmark: " + err.message;
            reposition();
        } finally {
            confirm.disabled = false;
            cancel.disabled = false;
        }
    });

    menu.replaceChildren(
        elem("div").classes("bookmarks-menu-label").text(`Delete “${cardTitle(card)}”?`),
        elem("div").classes("bookmarks-menu-actions").append(confirm, cancel),
        error
    );

    reposition();
    confirm.focus({ preventScroll: true });
}

function positionMenu(menu, clientX, clientY) {
    const margin = 8;

    menu.style.left = `${clientX}px`;
    menu.style.top = `${clientY}px`;

    const rect = menu.getBoundingClientRect();
    let left = clientX;
    let top = clientY;

    if (left + rect.width > window.innerWidth - margin) {
        left = Math.max(margin, window.innerWidth - rect.width - margin);
    }

    if (top + rect.height > window.innerHeight - margin) {
        top = Math.max(margin, window.innerHeight - rect.height - margin);
    }

    menu.style.left = `${Math.round(left)}px`;
    menu.style.top = `${Math.round(top)}px`;
}

function cardTitle(card) {
    return card.querySelector(".bookmarks-card-title")?.textContent.trim() ?? "";
}

function openAddEditor(control) {
    const location = gridLocation(control);
    if (location === null) return;

    openEditor({
        anchor: control,
        dialogLabel: "Add bookmark",
        endpoint: bookmarkEndpoint(location),
        method: "POST",
        submitLabel: "Add",
        extraBody: { "group-index": location.groupIndex },
        onSaved: async (response) => {
            const card = parseCard(await response.text());

            if (card !== null) {
                control.before(card);
            }
        },
    });
}

function openEditEditor(card, location) {
    openEditor({
        anchor: card,
        dialogLabel: "Edit bookmark",
        endpoint: bookmarkItemEndpoint(location),
        method: "PUT",
        submitLabel: "Save",
        initial: {
            title: cardTitle(card),
            url: card.href,
            description: card.querySelector(".bookmarks-card-description")?.textContent.trim() ?? "",
        },
        onSaved: async (response) => {
            const updated = parseCard(await response.text());

            if (updated !== null) {
                card.replaceWith(updated);
            }
        },
    });
}

function openEditor(options) {
    closeActivePopover();
    closeActiveMenu();

    const form = buildForm(options);
    const popover = elem("div")
        .classes("bookmarks-popover")
        .attrs({ role: "dialog", "aria-label": options.dialogLabel })
        .append(form.element);

    // The anchor stays in the layout so the popover can be positioned over its
    // slot, but is hidden while the popover is open.
    options.anchor.classList.add("bookmarks-anchor-hidden");

    const toggle = options.anchor.querySelector(".bookmarks-add-toggle");
    if (toggle !== null) toggle.setAttribute("aria-expanded", "true");

    document.body.append(popover);
    positionPopover(popover, options.anchor);

    let repositionQueued = false;
    const onReposition = () => {
        if (repositionQueued) return;

        repositionQueued = true;
        requestAnimationFrame(() => {
            repositionQueued = false;

            if (activePopover !== null && activePopover.element === popover) {
                positionPopover(popover, options.anchor);
            }
        });
    };

    const onDocumentMouseDown = (event) => {
        if (popover.contains(event.target) || options.anchor.contains(event.target)) return;

        closeActivePopover();
    };

    const onDocumentKeyDown = (event) => {
        if (event.key !== "Escape") return;

        event.preventDefault();
        closeActivePopover();

        if (toggle !== null) toggle.focus();
    };

    document.addEventListener("mousedown", onDocumentMouseDown, true);
    document.addEventListener("keydown", onDocumentKeyDown, true);
    window.addEventListener("resize", onReposition);
    window.addEventListener("scroll", onReposition, true);

    activePopover = {
        element: popover,
        anchor: options.anchor,
        onDocumentMouseDown: onDocumentMouseDown,
        onDocumentKeyDown: onDocumentKeyDown,
        onReposition: onReposition,
    };

    form.focus();
}

function closeActivePopover() {
    if (activePopover === null) return;

    const popover = activePopover;
    activePopover = null;

    document.removeEventListener("mousedown", popover.onDocumentMouseDown, true);
    document.removeEventListener("keydown", popover.onDocumentKeyDown, true);
    window.removeEventListener("resize", popover.onReposition);
    window.removeEventListener("scroll", popover.onReposition, true);

    popover.anchor.classList.remove("bookmarks-anchor-hidden");

    const toggle = popover.anchor.querySelector(".bookmarks-add-toggle");
    if (toggle !== null) toggle.setAttribute("aria-expanded", "false");

    popover.element.remove();
}

function positionPopover(popover, anchor) {
    const margin = 8;
    const rect = anchor.getBoundingClientRect();
    const viewportWidth = window.innerWidth;
    const viewportHeight = window.innerHeight;

    // Match the width of the card the popover expands from, so it looks like
    // the card itself turned into the form.
    const width = Math.min(Math.max(rect.width, 200), viewportWidth - margin * 2);

    popover.style.width = `${Math.round(width)}px`;

    const height = popover.offsetHeight;

    let left = rect.left;

    if (left + width > viewportWidth - margin) {
        left = viewportWidth - width - margin;
    }

    if (left < margin) {
        left = margin;
    }

    // Expand from the card's own position rather than below it, so the top of
    // the popover lines up with the neighboring cards.
    let top = rect.top;

    if (top + height > viewportHeight - margin) {
        top = Math.max(margin, viewportHeight - height - margin);
    }

    if (top < margin) {
        top = margin;
    }

    popover.style.left = `${Math.round(left)}px`;
    popover.style.top = `${Math.round(top)}px`;
}

function buildForm({ endpoint, method, submitLabel, initial, extraBody, onSaved }) {
    const error = elem("span").classes("bookmarks-form-error").attrs({ role: "alert" });

    const submit = elem("button")
        .classes("bookmarks-form-submit")
        .attrs({ type: "submit" })
        .text(submitLabel);

    const cancel = elem("button")
        .classes("bookmarks-form-cancel")
        .attrs({ type: "button" })
        .text("Cancel")
        .on("click", () => closeActivePopover());

    const titleInput = textInput("Title", initial?.title);
    const urlInput = textInput("https://example.com", initial?.url);
    const descriptionInput = textInput("Description", initial?.description);

    const form = elem("form")
        .classes("bookmarks-form")
        .on("keydown", (event) => {
            if (event.key !== "Tab") return;

            // Cycle between the fields and the buttons explicitly. Browsers
            // that do not put buttons in the tab order by default (e.g. Safari
            // without full keyboard access) would otherwise move focus out of
            // the page from the last field.
            const focusables = [titleInput, urlInput, descriptionInput, submit, cancel].filter((element) => {
                return !element.disabled;
            });

            const index = focusables.indexOf(document.activeElement);
            if (index === -1) return;

            const step = event.shiftKey ? -1 : 1;

            event.preventDefault();
            focusables[(index + step + focusables.length) % focusables.length].focus();
        })
        .on("submit", async (event) => {
            event.preventDefault();

            const title = titleInput.value.trim();
            const url = urlInput.value.trim();
            const description = descriptionInput.value.trim();

            if (title === "" || url === "") {
                error.textContent = "Title and URL are required";
                (title === "" ? titleInput : urlInput).focus();
                return;
            }

            error.textContent = "";
            submit.disabled = true;

            try {
                const body = { title: title, url: url, description: description };

                if (extraBody !== undefined) {
                    Object.assign(body, extraBody);
                }

                const response = await fetch(endpoint, {
                    method: method,
                    headers: { "Content-Type": "application/json" },
                    body: JSON.stringify(body),
                });

                if (!response.ok) {
                    error.textContent = (await response.text()).trim() || `Failed to save bookmark (${response.status})`;
                    return;
                }

                await onSaved(response);
                closeActivePopover();
            } catch (err) {
                error.textContent = "Failed to save bookmark: " + err.message;
            } finally {
                submit.disabled = false;
            }
        })
        .append(
            titleInput,
            urlInput,
            descriptionInput,
            elem("div")
                .classes("bookmarks-form-footer")
                .append(submit, cancel, error)
        );

    return {
        element: form,
        focus: () => {
            titleInput.focus();
            titleInput.select();
        },
    };
}

function textInput(placeholder, value) {
    const input = elem("input")
        .classes("bookmarks-form-input")
        .attrs({
            type: "text",
            placeholder: placeholder,
            autocomplete: "off",
            spellcheck: "false",
        });

    if (value !== undefined && value !== "") {
        input.value = value;
    }

    return input;
}

function parseCard(html) {
    const template = document.createElement("template");
    template.innerHTML = html.trim();

    return template.content.firstElementChild;
}

function bookmarkEndpoint({ bookmarksID }) {
    const baseURL = typeof pageData !== "undefined" ? pageData.baseURL : "";
    return `${baseURL}/api/widgets/${encodeURIComponent(bookmarksID)}/bookmarks`;
}

function bookmarkItemEndpoint(location) {
    return `${bookmarkEndpoint(location)}/${location.groupIndex}/${location.linkIndex}`;
}
