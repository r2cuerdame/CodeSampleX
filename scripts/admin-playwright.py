"""Private admin browser regression. Production mode is read-only by default.

Run the opt-in TestServeReportBrowserFixture on 127.0.0.1:18986 first for local
mutation coverage. Production credentials stay in memory, read from DPAPI;
screenshots and reports go to the specified local artifact directory.
"""
import argparse
import json
import subprocess
from pathlib import Path
from playwright.sync_api import sync_playwright, expect


def production_password():
    # Fixed file and origin; never place the credential in argv or artifacts.
    script = r"""$cipher = [IO.File]::ReadAllText("$env:LOCALAPPDATA/CodeSampleX/production-admin-password.dpapi").Trim()
$secret = ConvertTo-SecureString -String $cipher
$ptr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secret)
try { [Console]::Write([Runtime.InteropServices.Marshal]::PtrToStringBSTR($ptr)) }
finally { [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($ptr); $secret.Dispose() }
"""
    result = subprocess.run(["powershell", "-NoProfile", "-Command", script], capture_output=True, text=True)
    if result.returncode or not result.stdout:
        raise RuntimeError("Could not read the local DPAPI admin credential")
    return result.stdout


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--production', action='store_true')
    parser.add_argument('--baseline', action='store_true')
    parser.add_argument('--out', required=True)
    args = parser.parse_args()
    out = Path(args.out); out.mkdir(parents=True, exist_ok=True)
    origin = 'https://codesamplex.dev' if args.production else 'http://127.0.0.1:18986'
    password = production_password() if args.production else 'a-long-random-admin-secret'
    with sync_playwright() as p:
        browser = p.chromium.launch()
        context = browser.new_context(http_credentials={'username':'recuerdame','password':password,'origin':origin}, viewport={'width':1440,'height':1000})
        page = context.new_page(); errors=[]
        page.on('pageerror', lambda error: errors.append(str(error)))
        response = page.goto(origin+'/admin', wait_until='domcontentloaded')
        assert response.status == 200
        page.screenshot(path=str(out/'dashboard.png'), full_page=True)
        if args.baseline:
            (out/'baseline.txt').write_text(page.locator('body').inner_text(), encoding='utf-8')
            browser.close(); print('Authenticated baseline captured'); return
        page.get_by_role('tab', name='신고 처리', exact=True).click()
        expect(page.locator('#report-status')).not_to_contain_text('불러오는 중')
        expect(page.locator('#report-status')).not_to_contain_text('실패')
        page.screenshot(path=str(out/'reports-desktop.png'), full_page=True)
        if not args.production:
            expect(page.locator('#report-rows tr')).to_have_count(25)
            page.get_by_role('button',name='다음 25건').click()
            expect(page.locator('#report-rows tr')).to_have_count(6)
            page.get_by_label('신고 검색',exact=True).fill('component-00')
            page.get_by_role('button',name='검색',exact=True).click()
            expect(page.locator('#report-rows tr')).to_have_count(1)
            page.get_by_role('button',name='제품 #1',exact=True).click()
            expect(page.locator('#report-detail')).to_contain_text('<script>alert(1)</script>')
            expect(page.locator('#report-detail script')).to_have_count(0)
            page.locator('#report-detail select').select_option('expected-behavior')
            page.locator('#report-detail textarea').fill('Playwright measured escaped evidence, correct filtering and per-report review persistence.')
            page.once('dialog', lambda dialog: dialog.dismiss())
            page.get_by_role('button',name='검토 결과 저장',exact=True).click()
            expect(page.get_by_role('button',name='검토 결과 저장',exact=True)).to_be_enabled()
            page.once('dialog', lambda dialog: dialog.accept())
            page.get_by_role('button',name='검토 결과 저장',exact=True).click()
            expect(page.locator('#report-detail')).to_contain_text('expected-behavior')
            expect(page.locator('#report-detail textarea')).to_have_count(0)
            expect(page.locator('#report-rows tr')).to_have_count(0)
            page.reload(wait_until='domcontentloaded')
            expect(page.get_by_role('tab',name='신고 처리',exact=True)).to_have_attribute('aria-selected','true')
            page.locator('#report-filters [name=channel]').select_option('anomaly')
            expect(page.locator('#report-rows tr')).to_have_count(1)
            page.get_by_role('button',name='이상 #1',exact=True).click()
            expect(page.locator('#report-detail')).to_contain_text('독립 검증 영수증')
            expect(page.locator('#report-detail form')).to_have_count(0)
            # Read failure must clear the stale table and counts, then recover.
            page.route('**/admin/api/reports?*', lambda route: route.fulfill(status=503,body='unavailable'))
            page.get_by_role('button',name='새로고침',exact=True).click()
            expect(page.locator('#report-status')).to_contain_text('503')
            expect(page.locator('#report-rows tr')).to_have_count(0)
            page.unroute('**/admin/api/reports?*')
            page.get_by_role('button',name='새로고침',exact=True).click()
            expect(page.locator('#report-rows tr')).to_have_count(1)
        else:
            queue = context.request.get(origin+'/admin/api/reports?channel=all&state=all').json()
            details=[]
            for row in queue['rows']:
                response = context.request.get(origin+f"/admin/api/reports/{row['channel']}/{row['id']}")
                assert response.status == 200
                details.append(response.json())
            (out/'reports.json').write_text(json.dumps({'queue':queue,'details':details},indent=2),encoding='utf-8')
            if page.locator('#report-rows button').count():
                page.locator('#report-rows button').first.click()
                expect(page.locator('#report-detail')).to_be_visible()
            for path in ['/healthz','/version','/','/gaps']:
                response=context.request.get(origin+path)
                assert response.status == 200, (path,response.status)
        for width in [1440,390]:
            page.set_viewport_size({'width':width,'height':900})
            assert page.evaluate('document.documentElement.scrollWidth <= innerWidth'), f'overflow at {width}'
            page.screenshot(path=str(out/f'reports-{width}.png'),full_page=True)
        for name in ['대시보드','수요 · 진단','팜 · 토큰','신고 처리']:
            page.get_by_role('tab',name=name,exact=True).click()
            expect(page.get_by_role('tab',name=name,exact=True)).to_have_attribute('aria-selected','true')
            assert page.locator('.tabpanel:visible').count() == 1
        assert not errors, errors
        context.close()
        unauth=browser.new_context()
        assert unauth.request.get(origin+'/admin/api/reports').status == 401
        assert unauth.request.get(origin+'/admin').status == 401
        browser.close()
        (out/'result.json').write_text(json.dumps({'production':args.production,'passed':True,'pageErrors':errors},indent=2),encoding='utf-8')
        print('Admin Playwright verification passed')


if __name__ == '__main__':
    main()
