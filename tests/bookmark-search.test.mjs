// Run with: node --test tests/bookmark-search.test.mjs
import assert from "node:assert/strict";
import test from "node:test";
import { parseBookmarkQuery, matchesBookmarkQuery } from "../internal/glance/static/js/bookmark-search.js";

const bangs = new Map([
    ["!bm", { shortcut: "!bm", title: "Bookmarks", url: "bookmarks" }],
    ["!gg", { shortcut: "!gg", title: "Google", url: "https://google.test/?q=!QUERY!" }],
]);

test("ordinary searches and other bangs are not bookmark queries", () => {
    for (const value of ["", "github", "!gg github", "!yt video", "!b", "!bmx github", "!bm.github", "prefix !bm github"]) {
        assert.equal(parseBookmarkQuery(value, bangs), null, value);
    }
});

test("configured bookmark bangs are recognized without requiring a keyword", () => {
    for (const value of ["!bm", " !bm ", "!bm\t", "!bm\n"]) {
        assert.equal(parseBookmarkQuery(value, bangs), "", value);
    }
});

test("bookmark queries are case-insensitive and normalize whitespace", () => {
    assert.equal(parseBookmarkQuery("  !bm  GitHub\t Rust  ", bangs), "github rust");
    assert.equal(parseBookmarkQuery("!bm\nGitHub", bangs), "github");
    assert.equal(parseBookmarkQuery("!bm\u3000中文  书签", bangs), "中文 书签");
});

test("queries normalize fullwidth text and support non-Latin characters", () => {
    assert.equal(parseBookmarkQuery("!bm ＧｉｔＨｕｂ", bangs), "github");
    assert.equal(matchesBookmarkQuery("中文书签 https://example.com/", "中文"), true);
    assert.equal(matchesBookmarkQuery("GitHub", "ＧＩＴＨＵＢ"), true);
});

test("there is no implicit !bm shortcut without a matching configured URL", () => {
    assert.equal(parseBookmarkQuery("!bm github"), null);
    assert.equal(parseBookmarkQuery("!bm github", new Map()), null);
    assert.equal(parseBookmarkQuery("!gg github", bangs), null);
});

test("custom shortcuts are literal, case-sensitive and need no exclamation mark", () => {
    for (const shortcut of ["!b", "bm", "书签", "[b].+"]) {
        const custom = new Map([[shortcut, { shortcut, url: "bookmarks" }]]);
        assert.equal(parseBookmarkQuery(`  ${shortcut}	GitHub  Rust `, custom), "github rust");
        assert.equal(parseBookmarkQuery(`${shortcut}extra github`, custom), null);
        assert.equal(parseBookmarkQuery("!bm github", custom), null);
    }
    assert.equal(parseBookmarkQuery("!BM github", bangs), null);
});

test("only the exact special URL enables bookmark search", () => {
    for (const url of ["https://example.com/bookmarks", "Bookmarks", "bookmarks/", "bookmarks?q=!QUERY!"]) {
        const custom = new Map([["!bm", { shortcut: "!bm", url }]]);
        assert.equal(parseBookmarkQuery("!bm github", custom), null, url);
    }
});

test("every term must occur, in any order and across bookmark fields", () => {
    const text = "GitHub Code hosting https://github.com/example WORKS";
    assert.equal(matchesBookmarkQuery(text, "github works"), true);
    assert.equal(matchesBookmarkQuery(text, "WORKS hosting GitHub"), true);
    assert.equal(matchesBookmarkQuery(text, "github unknown"), false);
    assert.equal(matchesBookmarkQuery(text, "bitbucket"), false);
});

test("punctuation is matched literally rather than treated as a regex", () => {
    assert.equal(matchesBookmarkQuery("C++ guide https://example.com/c++?q=[rust]", "c++ [rust]"), true);
    assert.equal(matchesBookmarkQuery("any title", ".*"), false);
    assert.equal(matchesBookmarkQuery("a.b", "a.b"), true);
    assert.equal(matchesBookmarkQuery("axb", "a.b"), false);
});
