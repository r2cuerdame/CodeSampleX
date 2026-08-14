//! Json before State. Json is the extractor that consumes the request body, so
//! it has to be the last argument; everything before it is read from the head
//! of the request alone. This file must not compile.

use axum::extract::State;
use axum::routing::post;
use axum::{Json, Router};

async fn swapped(Json(_body): Json<String>, State(_state): State<u32>) {}

pub fn build() -> Router<u32> {
    Router::new().route("/items", post(swapped))
}
