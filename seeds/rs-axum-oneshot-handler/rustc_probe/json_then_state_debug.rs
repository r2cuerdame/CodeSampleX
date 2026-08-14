//! The same broken handler as json_then_state.rs with #[axum::debug_handler]
//! on it. Same code, same failure, completely different diagnostic - this is
//! the one that names the rule instead of the trait.

use axum::extract::State;
use axum::{Json, Router};
use axum::routing::post;

#[axum::debug_handler]
async fn swapped(Json(_body): Json<String>, State(_state): State<u32>) {}

pub fn build() -> Router<u32> {
    Router::new().route("/items", post(swapped))
}
