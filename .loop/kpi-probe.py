import json, sys, time, urllib.parse, urllib.request, urllib.error, datetime, statistics

Q = [
 ("npm","react","useState hook"),("npm","axios","get request"),("npm","typescript","compile options"),("npm","express","router middleware"),
 ("npm","lodash","debounce"),("npm","next","app router page"),("npm","vue","computed ref"),("npm","zod","object schema parse"),
 ("npm","dayjs","format date"),("npm","moment","parse date"),("npm","chalk","colored output"),("npm","commander","cli options"),
 ("npm","uuid","v4 generate"),("npm","dotenv","load env"),("npm","jest","mock function"),("npm","vitest","describe test"),
 ("npm","webpack","config loader"),("npm","vite","config plugin"),("npm","eslint","flat config"),("npm","prettier","format api"),
 ("npm","react-dom","createRoot render"),("npm","redux","configure store"),("npm","@reduxjs/toolkit","createSlice"),("npm","rxjs","observable pipe map"),
 ("npm","socket.io","emit event"),("npm","mongoose","schema model"),("npm","prisma","client query"),("npm","jsonwebtoken","sign verify"),
 ("npm","bcrypt","hash password"),("npm","yargs","parse args"),("npm","ws","websocket server"),("npm","node-fetch","fetch json"),
 ("npm","date-fns","format"),("npm","tailwindcss","config theme"),("npm","@tanstack/react-query","useQuery"),("npm","react-router-dom","createBrowserRouter"),
 ("npm","sharp","resize image"),("npm","pg","pool query"),("npm","redis","client get set"),("npm","fastify","route handler"),
 ("pypi","requests","get json"),("pypi","numpy","array reshape"),("pypi","pandas","read_csv groupby"),("pypi","flask","route json"),
 ("pypi","django","model query"),("pypi","fastapi","path operation"),("pypi","pydantic","BaseModel validate"),("pypi","sqlalchemy","session query"),
 ("pypi","pytest","fixture parametrize"),("pypi","httpx","async client"),("pypi","aiohttp","client session"),("pypi","boto3","s3 upload"),
 ("pypi","click","command option"),("pypi","pyyaml","safe_load"),("pypi","beautifulsoup4","find_all"),("pypi","scipy","optimize minimize"),
 ("pypi","matplotlib","plot savefig"),("pypi","scikit-learn","train_test_split fit"),("pypi","torch","tensor to device"),("pypi","pillow","image resize"),
 ("pypi","celery","task delay"),("pypi","redis","get set"),("pypi","psycopg2","connect cursor"),("pypi","jinja2","render template"),
 ("pypi","rich","console print"),("pypi","typer","command"),("pypi","uvicorn","run app"),("pypi","openai","chat completion"),
 ("pypi","python-dateutil","parse"),("pypi","cryptography","fernet encrypt"),("pypi","attrs","define class"),("pypi","tqdm","progress bar"),
 ("pypi","loguru","logger add"),("pypi","polars","read csv filter"),("pypi","selenium","webdriver find element"),
 ("golang","github.com/jackc/pgx/v5","connect query"),("golang","github.com/gin-gonic/gin","router json"),("golang","github.com/spf13/cobra","command flags"),
 ("golang","github.com/stretchr/testify","assert equal"),("golang","github.com/gorilla/mux","router handle"),("golang","go.uber.org/zap","logger"),
 ("golang","github.com/sirupsen/logrus","with fields"),("golang","github.com/spf13/viper","read config"),("golang","gorm.io/gorm","open create"),
 ("golang","github.com/google/uuid","new uuid"),("golang","github.com/redis/go-redis/v9","client get"),("golang","github.com/labstack/echo/v4","route handler"),
 ("golang","github.com/go-chi/chi/v5","router middleware"),("golang","google.golang.org/grpc","dial client"),("golang","google.golang.org/protobuf","proto marshal"),
 ("golang","github.com/golang-jwt/jwt/v5","parse token"),("golang","github.com/gorilla/websocket","upgrade conn"),("golang","github.com/prometheus/client_golang","counter register"),
 ("golang","golang.org/x/sync","errgroup"),("golang","github.com/urfave/cli/v2","app flags"),("golang","github.com/gofiber/fiber/v2","app get"),
 ("golang","github.com/aws/aws-sdk-go-v2","s3 client"),("golang","github.com/mattn/go-sqlite3","sql open"),("golang","github.com/rs/zerolog","log info"),
 ("golang","gopkg.in/yaml.v3","unmarshal"),
]

def purl(eco, name):
    return "pkg:%s/%s" % (eco, name.replace("@", "%40"))

def norm(p):
    p = urllib.parse.unquote(p.split("?")[0])
    if "@" in p[5:]:
        head = p[:5]; rest = p[5:]
        i = rest.rfind("@")
        if i > 0: rest = rest[:i]
        p = head + rest
    return p.lower()

def main(out, timeout):
    assert len(Q) == 100, len(Q)
    with open(out, "w", encoding="utf-8") as f:
        for eco, name, task in Q:
            q = "%s %s" % (name.split("/")[-1] if eco == "golang" else name, task)
            url = "https://codesamplex.dev/v2/search?" + urllib.parse.urlencode({"q": q, "package": purl(eco, name)})
            req = urllib.request.Request(url, headers={"User-Agent": "csx-kpi-probe/1125"})
            t0 = time.monotonic(); status = None; body = None; err = None
            try:
                with urllib.request.urlopen(req, timeout=timeout) as r:
                    status = r.status; body = r.read()
            except urllib.error.HTTPError as e:
                status = e.code; err = "http_%d" % e.code
            except Exception as e:
                err = "timeout" if "timed out" in str(e) else type(e).__name__
            dt = time.monotonic() - t0
            rec = {"eco": eco, "name": name, "q": q, "status": status, "err": err, "sec": round(dt, 3),
                   "at": datetime.datetime.now(datetime.timezone.utc).isoformat()}
            if body is not None and status == 200:
                try:
                    d = json.loads(body)
                    rec["grade"] = d.get("grade"); rec["miss"] = d.get("miss")
                    want = norm(purl(eco, name))
                    pk = [norm(p) for r in d.get("results", []) for p in r.get("case", {}).get("packages", [])]
                    rec["nresults"] = len(d.get("results", []))
                    rec["relevant"] = want in pk
                    rec["top"] = pk[:3]
                except Exception as e:
                    rec["err"] = "bad_json"
            f.write(json.dumps(rec, ensure_ascii=False) + "\n"); f.flush()
            print(len(open(out, encoding="utf-8").read().splitlines()), eco, name, rec.get("status"), rec.get("err"), rec["sec"], rec.get("grade"), rec.get("relevant"), flush=True)

if __name__ == "__main__":
    main(sys.argv[1], float(sys.argv[2]))
