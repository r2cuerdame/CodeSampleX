"""Local anonymous analytics browser regression; never reads production secrets.

Start TestServeAnonymousBrowserFixture with CSX_ANONYMOUS_BROWSER=1 first.
Run: python scripts/anonymous-admin-playwright.py --out <artifact-directory>
"""
import argparse
from pathlib import Path
from playwright.sync_api import sync_playwright, expect


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--out", required=True)
    args = parser.parse_args()
    out = Path(args.out)
    out.mkdir(parents=True, exist_ok=True)
    origin = "http://127.0.0.1:18987"
    with sync_playwright() as p:
        browser = p.chromium.launch()
        context = browser.new_context(
            http_credentials={"username": "recuerdame", "password": "local-anonymous-test-secret", "origin": origin},
            viewport={"width": 1440, "height": 1000},
        )
        # These unrelated fixture panels deliberately have no backing service.
        context.route("**/admin/api/**", lambda route: route.fulfill(status=503, body="fixture unavailable"))
        page = context.new_page()
        errors = []
        page.on("pageerror", lambda error: errors.append(str(error)))
        response = page.goto(origin + "/admin", wait_until="domcontentloaded")
        assert response.status == 200
        page.get_by_role("tab", name="수요 · 진단", exact=True).click()
        panel = page.locator("#anonymous-analytics")
        expect(panel).to_be_visible()
        expect(panel.locator(".trend svg")).to_have_count(7)
        expect(panel).to_contain_text("익명 활동 · 성공 API 요청")
        expect(panel).to_contain_text("IP는 식별에 사용하지 않습니다")
        assert panel.locator("circle").count() > 200
        panel.get_by_text("일별 코호트 유지율", exact=False).click()
        expect(panel.locator("table").first.locator("tbody tr")).not_to_have_count(0)
        assert panel.locator("table svg").count() > 0
        page.evaluate("window.scrollTo(0, 0)")
        page.screenshot(path=str(out / "anonymous-desktop.png"), full_page=True)
        page.reload(wait_until="domcontentloaded")
        expect(page.get_by_role("tab", name="수요 · 진단", exact=True)).to_have_attribute("aria-selected", "true")
        page.set_viewport_size({"width": 390, "height": 844})
        expect(panel).to_be_visible()
        assert page.evaluate("document.documentElement.scrollWidth <= window.innerWidth"), "mobile page overflow"
        page.evaluate("window.scrollTo(0, 0)")
        page.screenshot(path=str(out / "anonymous-mobile.png"), full_page=True)
        assert not errors, errors
        context.close()
        # Server rendered charts and accessible tables also work without JS.
        plain = browser.new_context(java_script_enabled=False, http_credentials={"username": "recuerdame", "password": "local-anonymous-test-secret", "origin": origin})
        raw = plain.new_page()
        raw.goto(origin + "/admin")
        assert raw.locator("#anonymous-analytics svg").count() >= 7
        plain.close()
        browser.close()
    print("PASS: authenticated analytics, four time-series, retention/cohort charts, tab persistence, mobile overflow, server-rendered charts")


if __name__ == "__main__":
    main()
