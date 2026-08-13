use serde::de::DeserializeOwned;
extern crate serde_json;

fn assert_impl<T: DeserializeOwned>() {}

#[test]
fn deserialize_owned_holds() {
    assert_impl::<i32>();
}
