# Issue 228 DevHotel acceptance: dependency-health fingerprint on a phone

The package page's dependency-health card quotes the first observed failure's
fingerprint whole — `sha256:` and 64 hex digits, 71 characters with no break
opportunity in a monospace face. On an iPhone that one `<code>` was wider than
the screen and dragged `documentElement.scrollWidth` open sideways; `body`'s
`overflow-x: hidden` hid the scrollbar without shrinking the overflow, so a
desktop review saw nothing.

Fix (`internal/web/static/site.css`): `.dephealth-break .break-evidence code
{ overflow-wrap: anywhere; }` plus `min-width: 0` on the card and break block.
Scoped to this one value on purpose — the dependency tables beside the card
scroll inside `.tablewrap`, and a global word-break would tear version numbers
apart in every table on the site.

## What was run

Room `43lue6qr` (CodeSampleX / issue-228, Node 22 image, no Go toolchain).
The real site ran in the room from a cross-compiled test binary:

```sh
# host
GOOS=linux GOARCH=amd64 go test -c -o /tmp/web.test ./internal/web
python -m http.server 8933 --bind 0.0.0.0        # serves /tmp
# room
curl -sS -o web.test http://host.docker.internal:8933/web.test
CSX_WEB_DEVSERVE=127.0.0.1:8932 CSX_WEB_DEVSERVE_STORE=dephealth \
  ./web.test -test.run TestDevServe -test.timeout 0 -test.v &
npm i -D playwright && npx playwright install --with-deps chromium   # 1.63.0
node acceptance.mjs                              # this directory's script
```

`CSX_WEB_DEVSERVE_STORE=dephealth` (`internal/web/devserve_test.go`) seeds
`/npm/axios?f_version=2.0.0` with a pinned release whose first observed failure
carries the full fingerprint, a same-receipt dependency failure and three
dependencies, so both dependency tables render beside the card.

`acceptance.mjs` opens the page with Playwright's `iPhone 14` (390) and
`iPhone 14 Pro Max` (430) device descriptors and a 1280×900 desktop context and
asserts, per target: `scrollWidth <= clientWidth`; the card ends inside the
viewport and is no wider than its section; the `<code>` still contains the
whole fingerprint, ends inside the viewport, and spans >1 line box on a phone
and exactly 1 on desktop; every `#dependency-health .tablewrap` computes
`overflow-x: auto|scroll` and ends inside the viewport.

## Result — 2026-09-19T03:22:22Z, binary sha256 `70095b81…2222c07` (branch head 917affc + dephealth fixture)

```
PASS iPhone 14 (390):         scrollWidth=390  clientWidth=390  fingerprintLines=2 full=true tables=auto(scrolls),auto(fits)
PASS iPhone 14 Pro Max (430): scrollWidth=430  clientWidth=430  fingerprintLines=2 full=true tables=auto(scrolls),auto(fits)
PASS desktop (1280):          scrollWidth=1280 clientWidth=1280 fingerprintLines=1 full=true tables=auto(fits),auto(fits)
```

Per-target geometry is in `acceptance-228.json`. At 390 the card is 358px wide
inside a 358px section; the fingerprint runs x=36…352 over two lines; the
health table is 552px of content scrolling inside a 356px `.tablewrap`
(`scrolls`) — the local horizontal scroll the issue asked to preserve. An
element screenshot of the card at a true 390px layout showed the complete
value on two lines with nothing clipped.

Negative control (same page, `overflow-wrap: normal !important` injected on the
scoped selector, i.e. the fix reverted in-page):

```
CONTROL (fix reverted) iPhone 14 (390):         scrollWidth=597 clientWidth=390 fingerprintLines=1 overflow=207px
CONTROL (fix reverted) iPhone 14 Pro Max (430): scrollWidth=597 clientWidth=430 fingerprintLines=1 overflow=167px
```

So the acceptance fails without the rule and passes with it.

The same measurements are a permanent Go regression,
`TestDependencyHealthFingerprintFitsNarrowViewports` in
`internal/web/dephealthoverflow_test.go`, which renders the real page in
headless Chrome at 320/360/390/430/1280 through fixed-width iframes (a headless
Chrome window will not size itself below ~500 CSS px; an iframe has no floor).
