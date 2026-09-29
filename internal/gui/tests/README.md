# Dropdown browser regression

`menu-scroll.test.cjs` loads the real HTML, CSS and JavaScript with isolated API
fixtures. It checks session folder/model filters and the main model picker in
Chromium and WebKit, including wheel, scrollbar track/thumb, keyboard selection
and dismissal. It does not start the backend or read local user configuration.

`click-scroll.test.cjs` holds the rule that a click never moves the page
(see "where the reader is" in `app.js`): on the Routing page, Live and a day
picked in turn with the list scrolled to its end; a click whose handler sets
scrollTop, grows the page above, redraws itself, or scrolls from a timer or a
shared helper; a click that asks to go somewhere with `scrollOnPurpose(e)`;
the room kept at the foot going as the reader scrolls back; and the wheel.

With Node.js and Playwright available:

```sh
node --test internal/gui/tests/menu-scroll.test.cjs internal/gui/tests/click-scroll.test.cjs
```

If Playwright is installed outside the repository, set `NODE_PATH` to the
directory containing its package. The suite uses Playwright's Chromium and
WebKit binaries (`playwright install chromium webkit`). No frontend dependency
is needed by the app itself. Tested with Playwright 1.63.0.

Set `BROWSER=chromium` or `BROWSER=webkit` for one engine. Set `ARTIFACT_DIR` to
an external directory to retain screenshots and Playwright traces, including
failed assertions. These browser checks run separately from `make test`.
