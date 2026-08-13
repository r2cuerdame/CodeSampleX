use serde::{
    Deserialize,
    Serialize,
};
use serde_json::json;
use local_helper::helper_fn;

#[derive(Serialize, Deserialize)]
struct Point {
    x: i32,
}

fn main() {
    helper_fn();
    let p = Point { x: 1 };
    let _ = json!({ "x": p.x });
}
