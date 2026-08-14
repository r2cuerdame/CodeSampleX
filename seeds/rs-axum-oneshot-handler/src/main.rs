//! Calling an axum handler as a function proves nothing. The routing, the
//! extractors, the rejections and the status codes all live in the Router, and
//! a hand-called handler skips every one of them. tower's ServiceExt::oneshot
//! feeds one Request through the whole Router in process and hands back the
//! Response: no listener, no port, no reserved port number, no I/O driver. The
//! runtime in main() is built with a plain .build() and no enable_all() so
//! that last part is checkable rather than claimed.
//!
//! Measured against axum 0.8.9 and rustc 1.97.1. Five traps, in the order they
//! hit you:
//!
//! 1. tower 0.5 declares no default features, so `tower = "0.5"` is a crate
//!    with no ServiceExt in it at all. See Cargo.toml.
//! 2. axum 0.8 changed path captures from `:id` to `{id}`. The old spelling is
//!    a panic while the Router is being built, not a route that quietly stops
//!    matching.
//! 3. "axum answers a bad JSON body with 422" is three different answers.
//!    Broken syntax is 400, a body that parses but does not fit the target
//!    type is 422, and a missing Content-Type is 415. One assertion for "bad
//!    input" is wrong on two of the three.
//! 4. The 404 from an unmatched route carries no headers whatsoever. Not an
//!    empty body with a content-length of zero: no content-length either.
//! 5. The extractor that consumes the body has to be the last argument.
//!    State or Path after Json is a compile error whose message never contains
//!    the word "order", and the extractors before it are tried in the order
//!    you wrote them, so their order decides which rejection the caller sees.
//!    rustc_probe/ holds the three variants and main() runs rustc on them.

use std::panic::AssertUnwindSafe;
use std::path::{Path as FsPath, PathBuf};
use std::process::Command;

use axum::body::Body;
use axum::extract::{Path, Query, State};
use axum::http::{header, HeaderMap, Method, Request, StatusCode};
use axum::response::Response;
use axum::routing::{get, post};
use axum::{Json, Router};
use http_body_util::BodyExt;
use serde::{Deserialize, Serialize};
use tower::ServiceExt;

// ------------------------------------------------------------- the service

/// Anything a handler needs that is not in the request. It is Clone because
/// axum hands each handler its own copy; in a real service this is where the
/// connection pool goes, behind an Arc.
#[derive(Clone)]
struct AppState {
    prefix: &'static str,
}

#[derive(Serialize)]
struct Item {
    id: String,
    name: String,
}

#[derive(Deserialize)]
struct NewItem {
    name: String,
    /// Required, and there is no #[serde(default)] on it. Leaving it out of
    /// the request body is the 422 below.
    #[allow(dead_code)]
    tags: Vec<String>,
}

#[derive(Deserialize)]
#[allow(dead_code)]
struct Filter {
    limit: u32,
}

/// State is read from the request head, Path from the matched route, so both
/// are FromRequestParts and either order compiles.
async fn get_item(State(state): State<AppState>, Path(id): Path<String>) -> Json<Item> {
    let name = format!("{}-{id}", state.prefix);
    Json(Item { id, name })
}

/// Json is last because it is the extractor that consumes the body. Swapping
/// these two arguments is rustc_probe/json_then_state.rs.
async fn create_item(
    State(state): State<AppState>,
    Json(body): Json<NewItem>,
) -> (StatusCode, Json<serde_json::Value>) {
    let name = format!("{}-{}", state.prefix, body.name);
    (StatusCode::CREATED, Json(serde_json::json!({ "name": name })))
}

/// Path before Query, with the body extractor last in both. These two handlers
/// are identical except for the order of their first two arguments, which is
/// the whole point of them.
async fn path_first(
    Path(id): Path<u32>,
    Query(_filter): Query<Filter>,
    Json(_body): Json<NewItem>,
) -> String {
    format!("path-first {id}")
}

async fn query_first(
    Query(_filter): Query<Filter>,
    Path(id): Path<u32>,
    Json(_body): Json<NewItem>,
) -> String {
    format!("query-first {id}")
}

fn app() -> Router {
    Router::new()
        .route("/items/{id}", get(get_item))
        .route("/items", post(create_item))
        .route("/path-first/{id}", post(path_first))
        .route("/query-first/{id}", post(query_first))
        // Until the state is supplied this is a Router<AppState>, and only
        // Router<()> is a Service. Forgetting with_state is a trait-bound
        // error at the oneshot() call site, a long way from the omission.
        .with_state(AppState { prefix: "widget" })
}

// -------------------------------------------------------------- the harness

/// One request in, the finished response out. This is the entire test harness.
async fn call(app: &Router, request: Request<Body>) -> (StatusCode, HeaderMap, String) {
    // oneshot() takes the service by value, which is why every call clones the
    // Router. Router is Clone so that a server can hand one out per
    // connection; a test gets to use the same property.
    let response: Response = app
        .clone()
        .oneshot(request)
        .await
        .expect("a Router's error type is Infallible, so this Result is a formality");

    let status = response.status();
    let headers = response.headers().clone();
    // The response body is a stream, not bytes, even when every byte of it is
    // already in memory. BodyExt::collect is the step people miss; it comes
    // from http-body-util rather than from axum or hyper. axum::body::to_bytes
    // does the same job and takes an explicit size limit.
    let bytes = response
        .into_body()
        .collect()
        .await
        .expect("an in-memory body cannot fail to collect")
        .to_bytes();
    (
        status,
        headers,
        String::from_utf8(bytes.to_vec()).expect("every body here is utf-8"),
    )
}

fn head(headers: &HeaderMap, name: header::HeaderName) -> Option<String> {
    headers
        .get(name)
        .map(|value| value.to_str().expect("header is ascii").to_string())
}

fn get_req(uri: &str) -> Request<Body> {
    Request::builder()
        .uri(uri)
        .body(Body::empty())
        .expect("valid request")
}

/// A POST with an optional Content-Type, because whether that header is
/// present changes the status code by itself.
fn post_req(uri: &str, content_type: Option<&str>, body: &'static str) -> Request<Body> {
    let mut builder = Request::builder().method(Method::POST).uri(uri);
    if let Some(content_type) = content_type {
        builder = builder.header(header::CONTENT_TYPE, content_type);
    }
    builder.body(Body::from(body)).expect("valid request")
}

// ------------------------------------------------------------- rustc probe

/// Compiles one file out of rustc_probe/ against the axum rlib cargo already
/// built for this run, and hands back rustc's exit status and stderr. No
/// network and no nested cargo: it reuses the artifacts in the target
/// directory, so it works under --network=none and takes no build lock.
fn compile_probe(file: &str) -> (bool, String) {
    let target = std::env::var_os("CARGO_TARGET_DIR")
        .map(PathBuf::from)
        .unwrap_or_else(|| FsPath::new(env!("CARGO_MANIFEST_DIR")).join("target"));

    let (deps, axum_rlib) = ["debug", "release"]
        .iter()
        .map(|profile| target.join(profile).join("deps"))
        .find_map(|dir| newest_rlib(&dir, "libaxum-").map(|rlib| (dir, rlib)))
        .unwrap_or_else(|| panic!("no libaxum-*.rlib under {}", target.display()));

    let out_dir = std::env::temp_dir().join("csx-axum-probe");
    std::fs::create_dir_all(&out_dir).expect("probe output directory");

    let source = FsPath::new(env!("CARGO_MANIFEST_DIR"))
        .join("rustc_probe")
        .join(file);

    let output = Command::new("rustc")
        .args(["--edition", "2021", "--crate-type", "lib", "--emit", "metadata"])
        .arg("--out-dir")
        .arg(&out_dir)
        // axum by path, everything axum itself needs by search path. The
        // proc-macro crate behind #[axum::debug_handler] is found here too.
        .arg("-L")
        .arg(format!("dependency={}", deps.display()))
        .arg("--extern")
        .arg(format!("axum={}", axum_rlib.display()))
        .arg(&source)
        .output()
        .expect("rustc is on PATH inside the toolchain image");

    (
        output.status.success(),
        String::from_utf8_lossy(&output.stderr).into_owned(),
    )
}

/// The newest matching rlib. A clean target directory holds exactly one, but
/// changing a feature in Cargo.toml leaves the previous build sitting next to
/// the new one, and probing the stale rlib produces an error about the feature
/// you just turned on rather than the one the probe is about.
fn newest_rlib(dir: &FsPath, prefix: &str) -> Option<PathBuf> {
    std::fs::read_dir(dir)
        .ok()?
        .flatten()
        .filter(|entry| {
            let name = entry.file_name();
            let name = name.to_string_lossy();
            name.starts_with(prefix) && name.ends_with(".rlib")
        })
        .max_by_key(|entry| {
            entry
                .metadata()
                .and_then(|meta| meta.modified())
                .expect("rlib metadata")
        })
        .map(|entry| entry.path())
}

/// Run `f` and hand back its panic message instead of letting the panic reach
/// the process. The hook is swapped out first because the panics here are
/// deliberate, and a backtrace printed by a passing contract is a false alarm
/// for whoever reads the log next.
fn panic_message<T>(f: impl FnOnce() -> T) -> Result<T, String> {
    let previous_hook = std::panic::take_hook();
    std::panic::set_hook(Box::new(|_| {}));
    let outcome = std::panic::catch_unwind(AssertUnwindSafe(f));
    std::panic::set_hook(previous_hook);
    outcome.map_err(|payload| {
        payload
            .downcast_ref::<String>()
            .cloned()
            .or_else(|| payload.downcast_ref::<&str>().map(|s| (*s).to_string()))
            .expect("axum's routing panics carry string payloads")
    })
}

// -------------------------------------------------------------------- main

fn main() {
    // No enable_all(). oneshot() never touches a socket or a timer, so the
    // runtime needs nothing but a scheduler, and tokio's "rt" feature alone is
    // enough to build one. A test that needs enable_all() to pass is a test
    // that is still doing real I/O somewhere.
    let rt = tokio::runtime::Builder::new_current_thread()
        .build()
        .expect("a current-thread runtime with no drivers");

    rt.block_on(async {
        let app = app();

        // ---- a GET route with a Path extractor ------------------------
        //
        // Nothing was stubbed: the router matched, State was cloned in, Path
        // deserialized the captured segment, and Json serialized the reply and
        // set the header. The body is compared byte for byte because a handler
        // that returns the right shape with the wrong field names still
        // returns 200.
        let (status, headers, body) = call(&app, get_req("/items/42")).await;
        assert_eq!(status, StatusCode::OK);
        assert_eq!(body, r#"{"id":"42","name":"widget-42"}"#);
        // axum's Json sets a bare application/json, with no charset parameter.
        assert_eq!(
            head(&headers, header::CONTENT_TYPE).as_deref(),
            Some("application/json"),
        );
        // A real content-length, computed from the real body.
        assert_eq!(
            head(&headers, header::CONTENT_LENGTH).as_deref(),
            Some(body.len().to_string().as_str()),
        );

        // A GET route answers HEAD as well, with the body dropped and the
        // headers kept. content-length still describes the body that a GET
        // would have returned, so a test that reads it instead of measuring
        // the bytes cannot tell the two verbs apart.
        let (status, head_headers, body) = call(
            &app,
            Request::builder()
                .method(Method::HEAD)
                .uri("/items/42")
                .body(Body::empty())
                .expect("valid request"),
        )
        .await;
        assert_eq!(status, StatusCode::OK);
        assert_eq!(body, "");
        assert_eq!(head(&head_headers, header::CONTENT_LENGTH).as_deref(), Some("30"));

        // ---- the POST body, and its three rejections ------------------
        //
        // The success case first, so the failures below are failures of the
        // input rather than of the route.
        let (status, headers, body) = call(
            &app,
            post_req("/items", Some("application/json"), r#"{"name":"a","tags":["t"]}"#),
        )
        .await;
        assert_eq!(status, StatusCode::CREATED);
        assert_eq!(body, r#"{"name":"widget-a"}"#);
        assert_eq!(
            head(&headers, header::CONTENT_TYPE).as_deref(),
            Some("application/json"),
        );

        // Truncated JSON. This is 400 BAD REQUEST, not 422: serde_json could
        // not finish parsing, so there was never a value to check against
        // NewItem. The "name:" in the middle of the message is the field path
        // that axum threads through serde_path_to_error, which is why the
        // sentence reads as though two messages were spliced together.
        let (status, headers, body) = call(&app, post_req("/items", Some("application/json"), r#"{"name":"#)).await;
        assert_eq!(status, StatusCode::BAD_REQUEST);
        assert_eq!(
            body,
            "Failed to parse the request body as JSON: name: EOF while parsing a value at line 1 column 8",
        );
        // The rejection body is plain text. A JSON API's error responses are
        // not JSON unless you replace the rejection yourself, so a client that
        // parses every body as JSON breaks on exactly the responses it most
        // needs to read.
        assert_eq!(
            head(&headers, header::CONTENT_TYPE).as_deref(),
            Some("text/plain; charset=utf-8"),
        );

        // Well-formed JSON that does not fit the type. This is the 422, and it
        // is the only one of the three that is: the document parsed, the shape
        // was refused.
        let (status, _, body) = call(
            &app,
            post_req("/items", Some("application/json"), r#"{"name":123,"tags":[]}"#),
        )
        .await;
        assert_eq!(status, StatusCode::UNPROCESSABLE_ENTITY);
        assert_eq!(
            body,
            "Failed to deserialize the JSON body into the target type: name: invalid type: integer `123`, expected a string at line 1 column 11",
        );

        // A missing required field lands in the same bucket, which is the
        // useful half of the split: 400 means the caller's serializer is
        // broken, 422 means the caller sent the wrong document.
        let (status, _, body) = call(
            &app,
            post_req("/items", Some("application/json"), r#"{"name":"a"}"#),
        )
        .await;
        assert_eq!(status, StatusCode::UNPROCESSABLE_ENTITY);
        assert_eq!(
            body,
            "Failed to deserialize the JSON body into the target type: missing field `tags` at line 1 column 12",
        );

        // No Content-Type at all is a third status again, and it is the one
        // that catches people writing requests by hand: the body is perfect
        // and the handler never runs.
        let (status, _, body) = call(&app, post_req("/items", None, r#"{"name":"a","tags":[]}"#)).await;
        assert_eq!(status, StatusCode::UNSUPPORTED_MEDIA_TYPE);
        assert_eq!(body, "Expected request with `Content-Type: application/json`");

        // That check is not string equality on the header, and it is not a
        // search for "json" either. axum parses the header as a mime type and
        // accepts it when the type is `application` and the subtype is `json`
        // or carries a `+json` suffix, so a charset parameter and a vendor type
        // both get through.
        for content_type in ["application/json; charset=utf-8", "application/vnd.csx+json"] {
            let (status, _, _) = call(
                &app,
                post_req("/items", Some(content_type), r#"{"name":"a","tags":[]}"#),
            )
            .await;
            assert_eq!(status, StatusCode::CREATED, "{content_type} should be accepted");
        }

        // The `application` half is load-bearing, which is where the rule stops
        // matching intuition: text/json reads like JSON to a person and is
        // refused exactly like a header that was never sent.
        let (status, _, body) = call(
            &app,
            post_req("/items", Some("text/json"), r#"{"name":"a","tags":[]}"#),
        )
        .await;
        assert_eq!(status, StatusCode::UNSUPPORTED_MEDIA_TYPE);
        assert_eq!(body, "Expected request with `Content-Type: application/json`");

        // ---- an unmatched route ---------------------------------------
        //
        // 404 with an empty body, and with nothing else either. The default
        // fallback is a bare status code, so there is no content-length and no
        // content-type to assert on - a test that checks for content-length: 0
        // is asserting a header that was never sent.
        let (status, headers, body) = call(&app, get_req("/nope")).await;
        assert_eq!(status, StatusCode::NOT_FOUND);
        assert_eq!(body, "");
        assert!(headers.is_empty(), "the default 404 sends no headers: {headers:?}");

        // ---- the right path, the wrong method -------------------------
        //
        // 405, and this one does carry headers: Allow lists what the path does
        // answer to. HEAD is in the list because the GET route serves it, and
        // the separator has no space after the comma, so splitting on ", "
        // gives you one entry named "GET,HEAD".
        let (status, headers, body) = call(
            &app,
            Request::builder()
                .method(Method::DELETE)
                .uri("/items/42")
                .body(Body::empty())
                .expect("valid request"),
        )
        .await;
        assert_eq!(status, StatusCode::METHOD_NOT_ALLOWED);
        assert_eq!(head(&headers, header::ALLOW).as_deref(), Some("GET,HEAD"));
        assert_eq!(head(&headers, header::CONTENT_LENGTH).as_deref(), Some("0"));
        assert_eq!(body, "");

        // The POST-only route lists only POST, so Allow describes the route
        // rather than the router.
        let (status, headers, _) = call(
            &app,
            Request::builder()
                .method(Method::PUT)
                .uri("/items")
                .body(Body::empty())
                .expect("valid request"),
        )
        .await;
        assert_eq!(status, StatusCode::METHOD_NOT_ALLOWED);
        assert_eq!(head(&headers, header::ALLOW).as_deref(), Some("POST"));

        // OPTIONS is not handled for you. It gets the same 405 with the
        // same Allow header, and Allow is not the header a browser preflight
        // reads - that is Access-Control-Allow-Methods, which nothing here
        // sets. The method list is right there under a name the browser
        // ignores, so the preflight fails on both counts.
        let (status, headers, _) = call(
            &app,
            Request::builder()
                .method(Method::OPTIONS)
                .uri("/items")
                .body(Body::empty())
                .expect("valid request"),
        )
        .await;
        assert_eq!(status, StatusCode::METHOD_NOT_ALLOWED);
        assert_eq!(head(&headers, header::ALLOW).as_deref(), Some("POST"));

        // ---- extractor order, at run time -----------------------------
        //
        // Everything before the body extractor is run in argument order and
        // the first rejection wins, so two handlers that differ only in the
        // order of their first two arguments answer the same broken request
        // differently. Both requests below are wrong in all three ways at
        // once: an id that is not a u32, no limit in the query string, and a
        // body that is not JSON.
        let (status, _, body) = call(&app, post_req("/path-first/abc", Some("application/json"), "{")).await;
        assert_eq!(status, StatusCode::BAD_REQUEST);
        assert_eq!(body, "Invalid URL: Cannot parse `abc` to a `u32`");

        let (status, _, body) = call(&app, post_req("/query-first/abc", Some("application/json"), "{")).await;
        assert_eq!(status, StatusCode::BAD_REQUEST);
        assert_eq!(body, "Failed to deserialize query string: missing field `limit`");

        // Which also means the body extractor's rejection is the one you see
        // least often: it only runs once everything ahead of it succeeded.
        let (status, _, body) = call(&app, post_req("/path-first/7?limit=3", Some("application/json"), "{")).await;
        assert_eq!(status, StatusCode::BAD_REQUEST);
        assert_eq!(
            body,
            "Failed to parse the request body as JSON: EOF while parsing an object at line 1 column 1",
        );
    });

    // ---- extractor order, at compile time -----------------------------
    //
    // The rule is not a lint and not a run-time check: an extractor that reads
    // the body implements FromRequest, everything else implements
    // FromRequestParts, and Handler is only implemented for functions whose
    // last argument is the FromRequest one. Correct order first, as the
    // control.
    let (compiled, stderr) = compile_probe("state_then_json.rs");
    assert!(compiled, "State before Json must compile: {stderr}");
    assert_eq!(stderr, "", "and compile without a warning");

    // Swap those two arguments and it stops compiling. The message is about a
    // missing trait implementation on the function item, it points at the
    // route() call rather than at the handler, and it does not contain the
    // words "body", "order" or "last".
    let (compiled, stderr) = compile_probe("json_then_state.rs");
    assert!(!compiled, "Json before State must not compile");
    assert!(
        stderr.starts_with(
            "error[E0277]: the trait bound `fn(Json<String>, State<u32>) -> \
             impl Future<Output = ()> {swapped}: Handler<_, _>` is not satisfied\n"
        ),
        "{stderr}",
    );
    for absent in ["body", "order", "last argument"] {
        assert!(!stderr.contains(absent), "expected {absent:?} to be missing from: {stderr}");
    }
    // The span sits on the route() line and nothing in the message points at
    // the handler's own line: the unsatisfied bound is at the registration, so
    // the argument that is in the wrong place is never underlined.
    assert!(stderr.contains("json_then_state.rs:12:40"), "{stderr}");
    assert!(!stderr.contains("json_then_state.rs:9:"), "{stderr}");

    // One suggestion in the whole message, and it costs a feature: "macros" is
    // not in axum 0.8.9's default list, which is why Cargo.toml has to ask for
    // it, so a default install is told to reach for an attribute it does not
    // have. Nothing else is offered - the message carries no help: line at all.
    assert!(stderr.contains("= note: Consider using `#[axum::debug_handler]` to improve the error message\n"));
    assert!(!stderr.contains("help:"), "{stderr}");

    // With the attribute on the same broken handler, rustc leads with a
    // sentence that names the rule and underlines the offending argument.
    // Same code, same failure, and the second error is still the unreadable
    // one - debug_handler adds a diagnostic rather than replacing it.
    let (compiled, stderr) = compile_probe("json_then_state_debug.rs");
    assert!(!compiled, "the annotated handler must not compile either");
    assert!(
        stderr.starts_with(
            "error: `Json<_>` consumes the request body and thus must be the last \
             argument to the handler function\n"
        ),
        "{stderr}",
    );
    // The underline is in the handler's signature rather than at the route:
    // line 10 is `async fn swapped`, column 31 its `Json<String>`.
    assert!(stderr.contains("json_then_state_debug.rs:10:31"), "{stderr}");
    assert!(stderr.contains("Handler<_, _>` is not satisfied"));

    // ---- the axum 0.7 path syntax -------------------------------------
    //
    // A router built with the old capture syntax does not fail to match at
    // run time; it never gets built. The panic is raised by route() itself and
    // names the replacement, so upgrading is mechanical once you have seen it
    // once - and impossible to miss, because the process dies at startup.
    let colon = panic_message(|| app_with_route("/items/:id"));
    assert_eq!(
        colon.err().expect("`:id` panics under axum 0.8"),
        "Path segments must not start with `:`. For capture groups, use `{capture}`. \
         If you meant to literally match a segment starting with a colon, call \
         `without_v07_checks` on the router.",
    );

    // Wildcards moved in the same release, with the brace on the outside of
    // the star rather than replacing it.
    let star = panic_message(|| app_with_route("/assets/*path"));
    assert_eq!(
        star.err().expect("`*path` panics under axum 0.8"),
        "Path segments must not start with `*`. For wildcard capture, use `{*wildcard}`. \
         If you meant to literally match a segment starting with an asterisk, call \
         `without_v07_checks` on the router.",
    );

    // And the new spellings build.
    app_with_route("/items/{id}");
    app_with_route("/assets/{*path}");

    println!(
        "CONTRACT PASS: oneshot drives the real Router with no port; \
         400/422/415 are three different bad bodies; a body extractor must be last"
    );
}

/// Builds and throws away a one-route Router. The interesting part is that
/// route() either returns or panics; the Router itself is never used.
fn app_with_route(path: &str) {
    let _router: Router = Router::new().route(path, get(|| async { "x" }));
}
