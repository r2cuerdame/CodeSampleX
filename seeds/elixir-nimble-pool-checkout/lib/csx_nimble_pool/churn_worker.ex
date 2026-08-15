defmodule CsxNimblePool.ChurnWorker do
  @moduledoc """
  A worker whose handle_checkout/4 always answers {:remove, ...}.

  This models the easy mistake of deciding to remove a worker based on the
  COMMAND (for example "this request needs a fresh connection") instead of on
  the state of the worker itself. The pool responds to :remove by discarding
  that worker and retrying the SAME checkout against another one, so a command
  that always removes turns into an unbounded terminate/init loop that only
  stops when the caller's own checkout deadline expires.

  Counts go through :atomics rather than messages because the loop is fast
  enough that a mailbox would be a poor place to keep the tally.
  """

  @behaviour NimblePool

  @inits 1
  @terminates 2

  @doc "Index of the init_worker counter inside the :atomics ref."
  def inits_index, do: @inits

  @doc "Index of the terminate_worker counter inside the :atomics ref."
  def terminates_index, do: @terminates

  @impl NimblePool
  def init_pool(counter), do: {:ok, counter}

  @impl NimblePool
  def init_worker(counter) do
    :atomics.add(counter, @inits, 1)
    # Slow the loop down so a 200 ms deadline produces a handful of cycles
    # rather than a pathological number of them.
    Process.sleep(5)
    {:ok, :resource, counter}
  end

  @impl NimblePool
  def handle_checkout(_command, _from, _worker_state, counter) do
    {:remove, :removed_by_command, counter}
  end

  @impl NimblePool
  def terminate_worker(_reason, _worker_state, counter) do
    :atomics.add(counter, @terminates, 1)
    {:ok, counter}
  end
end
