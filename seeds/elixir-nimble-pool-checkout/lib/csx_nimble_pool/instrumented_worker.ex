defmodule CsxNimblePool.InstrumentedWorker do
  @moduledoc """
  A NimblePool worker that reports each per-worker callback it runs to a named
  collector process, so the contract can assert WHICH callbacks fired.

  Several of the facts this seed pins down are negative — a callback that does
  NOT run — so the collector has to observe the whole callback set, not just
  the ones a successful checkout happens to use.
  """

  @behaviour NimblePool

  @collector :csx_pool_collector

  defp tell(message) do
    case Process.whereis(@collector) do
      nil -> :ok
      pid -> send(pid, message)
    end
  end

  # init_pool/1 runs once, in the pool process, and its return becomes the
  # pool_state threaded through every other callback. Without it, init_worker
  # would receive the raw worker arg instead.
  @impl NimblePool
  def init_pool(arg), do: {:ok, %{arg: arg, inits: 0}}

  @impl NimblePool
  def init_worker(pool_state) do
    n = pool_state.inits + 1
    tell({:init_worker, n})
    {:ok, {:worker, n}, %{pool_state | inits: n}}
  end

  @impl NimblePool
  def handle_checkout(command, _from, worker_state, pool_state) do
    tell({:handle_checkout, command, worker_state})

    case command do
      # :skip raises the exception in the CLIENT but leaves this worker
      # untouched and immediately reusable. It is not a worker failure.
      :skip ->
        {:skip, RuntimeError.exception("skipped by handle_checkout"), pool_state}

      _ ->
        {:ok, {:client_state, worker_state}, worker_state, pool_state}
    end
  end

  @impl NimblePool
  def handle_checkin(client_state, _from, worker_state, pool_state) do
    tell({:handle_checkin, client_state, worker_state})
    {:ok, worker_state, pool_state}
  end

  # Reached only through NimblePool.update/2, which is called from inside the
  # checkout! function, not from the pool.
  @impl NimblePool
  def handle_update(message, _worker_state, pool_state) do
    tell({:handle_update, message})
    {:ok, {:worker_updated_by, message}, pool_state}
  end

  # The spec for this callback returns a bare :ok, not {:ok, pool_state}; the
  # pool calls it for effect and discards the result.
  @impl NimblePool
  def handle_cancelled(context, _pool_state) do
    tell({:handle_cancelled, context})
    :ok
  end

  @impl NimblePool
  def terminate_worker(reason, worker_state, pool_state) do
    tell({:terminate_worker, reason, worker_state})
    {:ok, pool_state}
  end
end
