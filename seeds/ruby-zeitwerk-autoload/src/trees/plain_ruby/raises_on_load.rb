# Also outside every loader. This is the case where Ruby's autoload behaves
# differently: the require itself raises, so it never completes.
#
# The counter is a thread local rather than a constant because the contract
# measures how many times Ruby re-runs this file.
Thread.current[:csx_raises_on_load_runs] = Thread.current[:csx_raises_on_load_runs].to_i + 1
raise "load failed before any constant was defined"
