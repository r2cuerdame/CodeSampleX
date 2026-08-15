defmodule CsxNimblePoolTest do
  # async: false — every test registers itself under the same collector name
  # that the worker callbacks report to.
  use ExUnit.Case, async: false

  alias CsxNimblePool.ChurnWorker
  alias CsxNimblePool.InstrumentedWorker

  # The deliberate crashes below produce SASL error reports; keep them out of
  # the contract output.
  @moduletag :capture_log

  setup do
    Process.register(self(), :csx_pool_collector)
    :ok
  end

  defp start_pool!(opts) do
    opts
    |> Keyword.put_new(:worker, {InstrumentedWorker, :pool_arg})
    |> then(&start_supervised!({NimblePool, &1}))
  end

  describe "pool startup" do
    test ":pool_size defaults to 10, not System.schedulers_online()" do
      # The obvious guess is that a pool sizes itself to the scheduler count,
      # the way many BEAM pools do. NimblePool hardcodes 10 instead, so on a
      # machine with a different core count the pool is NOT the size you
      # assumed and the difference never shows up as an error.
      start_pool!([])

      for n <- 1..10 do
        assert_receive {:init_worker, ^n}, 1000
      end

      refute_receive {:init_worker, 11}, 200
    end

    test "lazy: true starts no workers until the first checkout" do
      pool = start_pool!(pool_size: 3, lazy: true)
      refute_receive {:init_worker, _}, 200

      assert :ok == NimblePool.checkout!(pool, :cmd, fn _from, cs -> {:ok, cs} end, 1000)

      assert_receive {:init_worker, 1}, 1000
      refute_receive {:init_worker, 2}, 200
    end
  end

  describe "the checkout! function" do
    test "runs in the CLIENT process and receives {pool_pid, ref}, not the client's from" do
      pool = start_pool!(pool_size: 1)
      test_pid = self()

      result =
        NimblePool.checkout!(pool, :cmd, fn from, client_state ->
          # GenServer.handle_call/3 hands you the CALLER's {pid, ref}. The
          # NimblePool equivalent is inverted: the pid is the POOL's. Code
          # that assumes `elem(from, 0)` is the caller will message the pool.
          assert {from_pid, from_ref} = from
          assert from_pid == pool
          refute from_pid == test_pid
          assert is_reference(from_ref)

          # The body executes in the caller, which is the whole design: the
          # resource is handed out rather than the work being sent in.
          assert self() == test_pid

          assert client_state == {:client_state, {:worker, 1}}
          {:the_result, {:handed_back, client_state}}
        end)

      # checkout! returns the FIRST element of the two-tuple; the second is
      # only ever seen by handle_checkin/4.
      assert result == :the_result
      assert_receive {:handle_checkin, {:handed_back, {:client_state, {:worker, 1}}}, {:worker, 1}},
                     1000
    end

    test "check-in is asynchronous but does not race immediate re-checkout" do
      # The docs say the check-in "happens asynchronously", which reads like a
      # single-worker pool cannot be reused back-to-back. It can: the pool
      # serialises the checkin message ahead of the next checkout request.
      pool = start_pool!(pool_size: 1)

      results =
        for i <- 1..25 do
          NimblePool.checkout!(pool, {:seq, i}, fn _from, cs -> {i, cs} end, 1000)
        end

      assert results == Enum.to_list(1..25)
    end
  end

  describe "failure semantics — the inversion" do
    test "RAISING inside the function is the safe failure: the pool replaces the worker" do
      pool = start_pool!(pool_size: 1)

      assert_raise RuntimeError, "boom", fn ->
        NimblePool.checkout!(pool, :cmd, fn _from, _cs -> raise "boom" end)
      end

      assert_receive {:handle_checkout, :cmd, {:worker, 1}}, 1000

      # checkout! catches the raise, tells the pool, and re-raises. The pool
      # discards the worker and builds a fresh one, so the pool self-heals.
      assert_receive {:handle_cancelled, :checked_out}, 1000
      assert_receive {:terminate_worker, :error, {:worker, 1}}, 1000
      assert_receive {:init_worker, 2}, 1000

      # A failed checkout is NOT checked back in.
      refute_receive {:handle_checkin, _, _}, 200

      assert :ok == NimblePool.checkout!(pool, :cmd, fn _f, cs -> {:ok, cs} end, 1000)
    end

    test "RETURNING a non two-tuple leaks the worker permanently while the caller lives" do
      pool = start_pool!(pool_size: 1)

      # The natural assumption is that a wrong return shape is the milder
      # mistake — worse formatting, same recovery. It is the opposite. The
      # cancel notification lives in the `catch` clause of checkout!, but this
      # error is raised by that try's `else` clause, and a try does not catch
      # what its own else raises. The pool is therefore never told anything.
      assert catch_error(
               NimblePool.checkout!(pool, :cmd, fn _from, _cs -> :forgot_the_tuple end)
             ) == {:try_clause, :forgot_the_tuple}

      assert_receive {:handle_checkout, :cmd, {:worker, 1}}, 1000

      # None of the recovery machinery runs.
      refute_receive {:handle_cancelled, _}, 300
      refute_receive {:terminate_worker, _, _}, 300
      refute_receive {:init_worker, 2}, 300
      refute_receive {:handle_checkin, _, _}, 300

      # The only worker is gone, so the pool is now a permanent timeout.
      assert catch_exit(NimblePool.checkout!(pool, :cmd, fn _f, cs -> {:ok, cs} end, 300)) ==
               {:timeout, {NimblePool, :checkout, [pool]}}
    end

    test "the leaked worker comes back only when the CALLER process dies" do
      # Reclamation is driven by the pool's monitor on the client, which is why
      # rescuing the try_clause error in a long-lived process is what makes the
      # leak permanent.
      pool = start_pool!(pool_size: 1)
      test_pid = self()

      caller =
        spawn(fn ->
          send(test_pid, :ready)
          NimblePool.checkout!(pool, :cmd, fn _from, _cs -> :forgot_the_tuple end)
        end)

      assert_receive :ready, 1000
      assert_receive {:handle_checkout, :cmd, {:worker, 1}}, 1000

      ref = Process.monitor(caller)
      assert_receive {:DOWN, ^ref, :process, ^caller, _reason}, 1000

      assert_receive {:handle_cancelled, :checked_out}, 1000
      assert_receive {:terminate_worker, :DOWN, {:worker, 1}}, 1000
      assert_receive {:init_worker, 2}, 1000
    end
  end

  describe "handle_checkout return values" do
    test "{:skip, exception, pool_state} raises in the client but keeps the worker" do
      pool = start_pool!(pool_size: 1)

      assert_raise RuntimeError, "skipped by handle_checkout", fn ->
        NimblePool.checkout!(pool, :skip, fn _from, cs -> {:ok, cs} end, 1000)
      end

      assert_receive {:handle_checkout, :skip, {:worker, 1}}, 1000

      # :skip is not a worker failure — nothing is terminated, nothing is
      # checked in, and worker 1 is still the one that serves the next call.
      refute_receive {:terminate_worker, _, _}, 300
      refute_receive {:handle_checkin, _, _}, 300

      assert :ok == NimblePool.checkout!(pool, :cmd, fn _f, cs -> {:ok, cs} end, 1000)
      assert_receive {:handle_checkout, :cmd, {:worker, 1}}, 1000
    end

    test "{:remove, reason, pool_state} retries the same checkout, so a command-keyed remove spins" do
      counter = :atomics.new(2, [])
      pool_size = 2

      pool =
        start_supervised!(
          {NimblePool, worker: {ChurnWorker, counter}, pool_size: pool_size}
        )

      # The caller does not get an error explaining the removals; it just waits
      # out its own deadline while the pool burns workers behind it.
      assert catch_exit(NimblePool.checkout!(pool, :cmd, fn _f, cs -> {:ok, cs} end, 200)) ==
               {:timeout, {NimblePool, :checkout, [pool]}}

      inits = :atomics.get(counter, ChurnWorker.inits_index())
      terminates = :atomics.get(counter, ChurnWorker.terminates_index())

      # More workers were built than the pool was ever sized for: every
      # :remove destroyed one and the pool replaced it to retry.
      assert inits > pool_size
      assert terminates >= pool_size
    end
  end

  describe "cancellation contexts and terminate reasons" do
    test "a queued timeout is :queued; a crash holding a worker is :checked_out with reason :DOWN" do
      pool = start_pool!(pool_size: 1)
      test_pid = self()

      holder =
        spawn(fn ->
          NimblePool.checkout!(
            pool,
            :hold,
            fn _from, cs ->
              send(test_pid, :holding)
              Process.sleep(30_000)
              {:ok, cs}
            end,
            30_000
          )
        end)

      assert_receive :holding, 2000
      assert_receive {:handle_checkout, :hold, {:worker, 1}}, 1000

      # This caller never gets a worker at all, so its cancellation is :queued
      # and nothing is torn down.
      assert catch_exit(NimblePool.checkout!(pool, :cmd, fn _f, cs -> {:ok, cs} end, 150)) ==
               {:timeout, {NimblePool, :checkout, [pool]}}

      assert_receive {:handle_cancelled, :queued}, 1000
      refute_receive {:terminate_worker, _, _}, 300

      # The holder, which does have a worker, cancels as :checked_out, and the
      # terminate reason for a dead client is the atom :DOWN.
      Process.exit(holder, :kill)

      assert_receive {:handle_cancelled, :checked_out}, 1000
      assert_receive {:terminate_worker, :DOWN, {:worker, 1}}, 1000
      assert_receive {:init_worker, 2}, 1000
    end
  end

  describe "NimblePool.update/2" do
    test "update/2 is called from the client and rewrites the worker_state seen by handle_checkin" do
      # There is no public checkin function, so update/2 is the only way to
      # change the pool's view of a resource mid-checkout. It takes the `from`
      # value, which is why that argument is worth keeping.
      pool = start_pool!(pool_size: 1)

      assert :done ==
               NimblePool.checkout!(pool, :cmd, fn from, client_state ->
                 assert :ok == NimblePool.update(from, :new_socket)
                 {:done, client_state}
               end)

      assert_receive {:handle_update, :new_socket}, 1000

      # handle_checkin sees the worker_state that handle_update returned, while
      # the client_state it receives is untouched by the update.
      assert_receive {:handle_checkin, {:client_state, {:worker, 1}},
                      {:worker_updated_by, :new_socket}},
                     1000
    end
  end
end
