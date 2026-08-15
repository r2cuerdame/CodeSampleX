# This is what would live in config/test.exs in a real app: the production
# module reads its Req options out of the application env, and the test
# environment is the only place that points them at a stub.
#
# retry_delay/retry_log_level are test-only. Req's retry step sleeps with
# exponential backoff and jitter (roughly 0.949s, 1.97s, 3.87s) and logs a
# warning before each attempt, so a suite that exercises retries without
# these two options is both slow and noisy.
Application.put_env(:csx_req, :req_options,
  plug: {Req.Test, CsxReq.Weather},
  retry_delay: 0,
  retry_log_level: false
)

ExUnit.start()
