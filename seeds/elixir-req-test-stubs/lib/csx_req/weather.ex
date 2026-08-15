defmodule CsxReq.Weather do
  @moduledoc """
  Production code that calls an HTTP JSON API, written so a test can replace
  the network without editing this file.

  The only test seam is the `:req_options` application env: empty in
  production (or carrying the API key), and in tests carrying
  `plug: {Req.Test, CsxReq.Weather}`. That tuple is a plug: `Req.Test` itself
  implements the Plug behaviour, and the second element is the *name* a stub
  is registered under. `Req.Test.call/2` looks the name up in an ownership
  registry keyed by the calling process — the same model Mox uses — which is
  why several `async: true` tests can register different stubs under the same
  name at once.

  Look at what `get_rating/1` matches on: `body: %{"celsius" => celsius}`, an
  already-decoded map. Nothing here calls a `.json()` accessor, and that is
  the difference from httpx's MockTransport or Guzzle's MockHandler. Req's
  `decode_body` response step decodes the body from the *response*
  content-type, so `Req.Test.json/2` in the stub is what makes this clause
  match. Send the same bytes without a content-type, or set
  `decode_body: false`, and `body` is a binary, this clause does not match,
  and `get_rating/1` returns `:error` for what is still a clean HTTP 200.
  """

  @doc """
  Classifies the current temperature, or `:error` if the call did not produce
  a 200 with a decoded JSON object.
  """
  def get_rating(location) do
    case get_temperature(location) do
      {:ok, %{status: 200, body: %{"celsius" => celsius}}} ->
        cond do
          celsius < 18.0 -> {:ok, :too_cold}
          celsius < 30.0 -> {:ok, :nice}
          true -> {:ok, :too_hot}
        end

      _ ->
        :error
    end
  end

  @doc """
  Returns `{:ok, %Req.Response{}}` or `{:error, exception}`.

  An HTTP 500 is `{:ok, response}`: `Req.request/1` only returns `:error` for
  things that stopped the exchange happening at all, such as a transport
  error. `:http_errors` is the option that changes that, and its default is
  `:return`.
  """
  def get_temperature(location, overrides \\ []) do
    [base_url: "https://weather.example.com", params: [location: location]]
    |> Keyword.merge(Application.get_env(:csx_req, :req_options, []))
    |> Keyword.merge(overrides)
    |> Req.request()
  end
end
