defmodule CsxJsonTest do
  use ExUnit.Case

  # Elixir 1.18 put a JSON module in the standard library, so "do I still
  # need Jason?" became a real question. The answer is mostly yes-you-can,
  # with two exceptions that are easy to miss because they are not encode
  # differences — they are the option set and the exception module.

  test "stdlib JSON is present and agrees with Jason byte for byte" do
    assert Code.ensure_loaded?(JSON)
    payload = %{"b" => 2, "a" => 1, "nested" => %{"list" => [1, "two", nil, true]}}
    assert JSON.encode!(payload) == Jason.encode!(payload)
    assert JSON.decode!(JSON.encode!(payload)) == payload
  end

  test "the exception module changes, so a rescue clause stops matching" do
    # This is the migration's quiet failure: the code still compiles, the
    # rescue still reads correctly, and it no longer catches anything.
    assert_raise Jason.DecodeError, fn -> Jason.decode!("{oops}") end

    assert_raise JSON.DecodeError, fn ->
      try do
        JSON.decode!("{oops}")
      rescue
        e in Jason.DecodeError -> flunk("a Jason rescue caught it: " <> inspect(e))
      end
    end
  end

  test "the tuple form does not carry a struct, so that clause stops matching too" do
    assert {:ok, %{"a" => 1}} = JSON.decode(~s({"a":1}))
    # Measured, and the opposite of what the symmetry suggests: JSON.decode/1
    # returns a bare reason tuple. Only decode!/1 raises the struct, so a
    # `{:error, %JSON.DecodeError{}}` clause never matches either — the
    # error path has to be rewritten, not renamed.
    assert {:error, {:invalid_byte, 1, ?o}} = JSON.decode("{oops}")
    assert {:error, %Jason.DecodeError{}} = Jason.decode("{oops}")
  end

  test "decoding straight to atom keys has no stdlib equivalent" do
    assert Jason.decode!(~s({"a":1}), keys: :atoms) == %{a: 1}
    # JSON.decode/2 does not exist, so there is nowhere to pass an option.
    # An application relying on keys: :atoms cannot drop Jason by swapping
    # the module name.
    refute function_exported?(JSON, :decode, 2)
  end

  test "pretty printing has no stdlib equivalent either" do
    assert Jason.encode!(%{"a" => 1}, pretty: true) =~ "\n"
    refute JSON.encode!(%{"a" => 1}) =~ "\n"
  end

  test "both stringify non-string keys and both refuse tuples" do
    assert JSON.encode!(%{1 => "x"}) == ~s({"1":"x"})
    assert JSON.encode!(%{1 => "x"}) == Jason.encode!(%{1 => "x"})
    assert_raise Protocol.UndefinedError, fn -> JSON.encode!({1, 2}) end
    assert_raise Protocol.UndefinedError, fn -> Jason.encode!({1, 2}) end
  end

  test "encode_to_iodata! avoids the final binary join" do
    assert is_list(JSON.encode_to_iodata!(%{"a" => 1}))
    assert IO.iodata_to_binary(JSON.encode_to_iodata!(%{"a" => 1})) == ~s({"a":1})
  end
end
