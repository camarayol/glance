import { elem } from "./templating.js";

let activePopover = null;
let listenerAttached = false;

export function setup() {
    const controls = document.getElementsByClassName("bookmarks-add");

    for (let i = 0; i < controls.length; i++) {
        decorateControl(controls[i]);
    }

    if (listenerAttached) return;

    listenerAttached = true;
    document.addEventListener("click", onDocumentClick);
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

    if (activePopover !== null && activePopover.control === control) {
        closeActivePopover();
        return;
    }

    decorateControl(control);
    openPopover(control);
}

function closeActivePopover() {
    if (activePopover === null) return;

    const popover = activePopover;
    activePopover = null;

    document.removeEventListener("mousedown", popover.onDocumentMouseDown, true);
    document.removeEventListener("keydown", popover.onDocumentKeyDown, true);
    window.removeEventListener("resize", popover.onReposition);
    window.removeEventListener("scroll", popover.onReposition, true);

    popover.control.classList.remove("bookmarks-add-open");
    popover.control.querySelector(".bookmarks-add-toggle")?.setAttribute("aria-expanded", "false");

    popover.element.remove();
}

function openPopover(control) {
    closeActivePopover();

    const bookmarksID = control.dataset.bookmarksId;
    const groupIndex = parseInt(control.dataset.groupIndex, 10);

    if (!bookmarksID || Number.isNaN(groupIndex)) return;

    const form = buildForm(bookmarksID, groupIndex, control);
    const popover = elem("div")
        .classes("bookmarks-add-popover")
        .attrs({ role: "dialog", "aria-label": "Add bookmark" })
        .append(form.element);

    // The control stays in the layout (so the popover can be positioned over
    // its slot) but is hidden while the popover is open.
    control.classList.add("bookmarks-add-open");
    control.querySelector(".bookmarks-add-toggle")?.setAttribute("aria-expanded", "true");

    document.body.append(popover);
    positionPopover(popover, control);

    let repositionQueued = false;
    const onReposition = () => {
        if (repositionQueued) return;

        repositionQueued = true;
        requestAnimationFrame(() => {
            repositionQueued = false;

            if (activePopover !== null && activePopover.element === popover) {
                positionPopover(popover, control);
            }
        });
    };

    const onDocumentMouseDown = (event) => {
        if (popover.contains(event.target) || control.contains(event.target)) return;

        closeActivePopover();
    };

    const onDocumentKeyDown = (event) => {
        if (event.key !== "Escape") return;

        event.preventDefault();
        closeActivePopover();

        const toggle = control.querySelector(".bookmarks-add-toggle");
        if (toggle !== null) toggle.focus();
    };

    document.addEventListener("mousedown", onDocumentMouseDown, true);
    document.addEventListener("keydown", onDocumentKeyDown, true);
    window.addEventListener("resize", onReposition);
    window.addEventListener("scroll", onReposition, true);

    activePopover = {
        element: popover,
        control: control,
        onDocumentMouseDown: onDocumentMouseDown,
        onDocumentKeyDown: onDocumentKeyDown,
        onReposition: onReposition,
    };

    form.focus();
}

function positionPopover(popover, control) {
    const margin = 8;
    const rect = control.getBoundingClientRect();
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

function buildForm(bookmarksID, groupIndex, control) {
    const error = elem("span").classes("bookmarks-add-error").attrs({ role: "alert" });

    const submit = elem("button")
        .classes("bookmarks-add-submit")
        .attrs({ type: "submit" })
        .text("Add");

    const cancel = elem("button")
        .classes("bookmarks-add-cancel")
        .attrs({ type: "button" })
        .text("Cancel")
        .on("click", () => closeActivePopover());

    const titleInput = textInput("Title");
    const urlInput = textInput("https://example.com");
    const descriptionInput = textInput("Description");

    const form = elem("form")
        .classes("bookmarks-add-form")
        .on("submit", (event) => {
            event.preventDefault();
            submitBookmark(titleInput, urlInput, descriptionInput, submit, error, control);
        })
        .append(
            titleInput,
            urlInput,
            descriptionInput,
            elem("div")
                .classes("bookmarks-add-footer")
                .append(submit, cancel, error)
        );

    async function submitBookmark(titleInput, urlInput, descriptionInput, submit, error, control) {
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
            const response = await fetch(bookmarksEndpoint(bookmarksID), {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({
                    "group-index": groupIndex,
                    title: title,
                    url: url,
                    description: description,
                }),
            });

            if (!response.ok) {
                const message = (await response.text()).trim();
                error.textContent = message || `Failed to add bookmark (${response.status})`;
                return;
            }

            const template = document.createElement("template");
            template.innerHTML = (await response.text()).trim();

            const card = template.content.firstElementChild;
            if (card) {
                control.before(card);
            }

            // Adding a single bookmark is the whole point of the popover, so
            // close it once the bookmark has been saved.
            closeActivePopover();
        } catch (err) {
            error.textContent = "Failed to add bookmark: " + err.message;
        } finally {
            submit.disabled = false;
        }
    }

    return {
        element: form,
        focus: () => titleInput.focus(),
    };
}

function textInput(placeholder) {
    return elem("input")
        .classes("bookmarks-add-input")
        .attrs({
            type: "text",
            placeholder: placeholder,
            autocomplete: "off",
            spellcheck: "false",
        });
}

function bookmarksEndpoint(bookmarksID) {
    const baseURL = typeof pageData !== "undefined" ? pageData.baseURL : "";
    return `${baseURL}/api/widgets/${bookmarksID}/bookmarks`;
}
