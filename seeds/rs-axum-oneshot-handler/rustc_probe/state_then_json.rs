//! The control for json_then_state.rs: the same two extractors, the body one
//! last, registered on the same Router. This file must compile clean.

use axum::extract::State;
use axum::routing::post;
use axum::{Json, Router};

async fn ordered(State(_state): State<u32>, Json(_body): Json<String>) {}

pub fn build() -> Router<u32> {
    Router::new().route("/items", post(ordered))
}
