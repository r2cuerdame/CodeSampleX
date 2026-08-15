# frozen_string_literal: true

require "minitest/autorun"
require_relative "../src/inventory"

class SorbetRuntimeChecksTest < Minitest::Test
  # ------------------------------------------------------------------
  # 1. A sig is shallow on collections.
  # ------------------------------------------------------------------
  def test_a_sig_typed_array_of_integer_admits_an_array_of_strings
    # The obvious wrong belief: "sorbet-runtime raises TypeError here."
    # It does not. valid? on a T::Types::TypedArray is `is_a?(Array)`.
    assert_equal %w[a b], Inventory::Gate.admit(%w[a b])
  end

  def test_the_outer_class_of_a_collection_is_still_checked
    # So the sig is not decorative -- it just stops one level down.
    err = assert_raises(TypeError) { Inventory::Gate.admit("not an array") }
    assert_match(/Expected type T::Array\[Integer\]/, err.message)
    assert_match(/got type String/, err.message)
  end

  def test_a_hash_sig_is_shallow_in_both_key_and_value
    shallow = T::Hash[Symbol, Integer]
    assert shallow.valid?({ "k" => "v" })
    refute shallow.recursively_valid?({ "k" => "v" })
  end

  # ------------------------------------------------------------------
  # 2. T.let and T.cast are BOTH checked, and both are shallow.
  # ------------------------------------------------------------------
  def test_t_cast_is_checked_at_runtime_exactly_like_t_let
    # The obvious wrong belief: T.let is checked and T.cast is an unchecked
    # assertion you use to shut the static checker up. Both raise, and the
    # only difference in the failure is the prefix on the message.
    let_err = assert_raises(TypeError) { T.let("s", Integer) }
    cast_err = assert_raises(TypeError) { T.cast("s", Integer) }
    assert_match(/\AT\.let: Expected type Integer/, let_err.message)
    assert_match(/\AT\.cast: Expected type Integer/, cast_err.message)
  end

  def test_t_unsafe_is_the_one_that_checks_nothing
    assert_equal "s", T.unsafe("s")
  end

  def test_t_let_and_t_cast_are_shallow_on_collections_too
    # Reaching for T.let at a boundary does not buy the element check back.
    assert_equal %w[a], T.let(%w[a], T::Array[Integer])
    assert_equal %w[a], T.cast(%w[a], T::Array[Integer])
  end

  # ------------------------------------------------------------------
  # 3. The same type as a T::Struct prop IS deep.
  # ------------------------------------------------------------------
  def test_a_struct_prop_checks_every_element_on_new
    err = assert_raises(TypeError) { Inventory::Order.new(id: 1, item_ids: %w[a]) }
    assert_match(/Can't set Inventory::Order\.item_ids/, err.message)
    assert_match(/need a T::Array\[Integer\]/, err.message)
  end

  def test_a_struct_prop_checks_every_element_on_the_setter
    order = Inventory::Order.new(id: 1, item_ids: [7])
    assert_raises(TypeError) { order.item_ids = [7, "eight"] }
    assert_equal [7], order.item_ids
  end

  def test_the_struct_error_names_the_bad_container_not_the_bad_element
    # Worth knowing when you read the failure: it reports the whole array,
    # so a 500-element array with one bad entry gives you no index.
    err = assert_raises(TypeError) { Inventory::Order.new(id: 1, item_ids: [1, 2, "3"]) }
    assert_match(/\[1, 2, "3"\]/, err.message)
  end

  # ------------------------------------------------------------------
  # 4. from_hash re-opens the hole the constructor closed.
  # ------------------------------------------------------------------
  def test_from_hash_skips_the_element_check_the_constructor_enforces
    smuggled = Inventory::Order.from_hash({ "id" => 1, "item_ids" => %w[a] })
    assert_equal %w[a], smuggled.item_ids
    # Same object, same prop, and its own constructor would have refused it.
    assert_raises(TypeError) { Inventory::Order.new(id: 1, item_ids: %w[a]) }
  end

  def test_from_hash_wants_string_keys
    # Symbol keys do not raise a helpful error, they read as absent props.
    assert_equal 1, Inventory::Order.from_hash({ "id" => 1, "item_ids" => [] }).id
    err = assert_raises(RuntimeError) do
      Inventory::Order.from_hash({ id: 1, item_ids: [] })
    end
    assert_match(/deserialize a required prop from a nil value/, err.message)
  end

  # ------------------------------------------------------------------
  # 5. recursively_valid? is the deep check; valid? and validate! are not.
  # ------------------------------------------------------------------
  def test_recursively_valid_is_the_deep_check
    type = T::Array[Integer]
    assert_instance_of T::Types::TypedArray, type
    assert type.valid?(%w[a]), "valid? stops at the outer class"
    refute type.recursively_valid?(%w[a]), "recursively_valid? walks the elements"
    assert type.recursively_valid?([1, 2])
  end

  def test_validate_bang_follows_the_shallow_rule
    # validate! raises on a wrong container and stays silent on wrong
    # elements, so it is not the deep guard its name suggests.
    assert_nil T::Array[Integer].validate!(%w[a])
    assert_raises(TypeError) { T::Array[Integer].validate!("nope") }
  end

  def test_the_deep_guard_in_the_sample_rejects_what_the_sig_admits
    assert_equal [1, 2], Inventory::Gate.admit_deeply([1, 2])
    err = assert_raises(TypeError) { Inventory::Gate.admit_deeply(%w[a]) }
    assert_match(/expected T::Array\[Integer\]/, err.message)
  end

  # ------------------------------------------------------------------
  # 6. A prop default is copied per instance.
  # ------------------------------------------------------------------
  def test_a_mutable_prop_default_is_not_shared_between_instances
    first = Inventory::Order.new(id: 1, item_ids: [])
    first.tags << "urgent"
    second = Inventory::Order.new(id: 2, item_ids: [])
    assert_equal ["urgent"], first.tags
    assert_empty second.tags
    refute_same first.tags, second.tags
  end

  # ------------------------------------------------------------------
  # 7. T::Struct is not a value object.
  # ------------------------------------------------------------------
  def test_two_identical_structs_are_not_equal
    # The obvious wrong belief: it is called a Struct, so == compares props.
    # T::Struct defines no ==, no eql? and no hash, so it falls back to
    # identity and silently misses as a Hash key or in Array#uniq.
    a = Inventory::Order.new(id: 1, item_ids: [7])
    b = Inventory::Order.new(id: 1, item_ids: [7])
    refute_equal a, b
    refute a.eql?(b)
    assert_nil({ a => "x" }[b])
    assert_equal 2, [a, b].uniq.size
    refute Inventory::Order.ancestors.include?(::Struct)
  end

  def test_serialize_is_how_you_compare_two_structs
    a = Inventory::Order.new(id: 1, item_ids: [7])
    b = Inventory::Order.new(id: 1, item_ids: [7])
    assert_equal a.serialize, b.serialize
    assert_equal({ "id" => 1, "item_ids" => [7], "tags" => [] }, a.serialize)
  end

  # ------------------------------------------------------------------
  # 8. A sig is lazy: it is built on the first call, not at load.
  # ------------------------------------------------------------------
  def test_a_sig_is_not_built_until_the_method_is_first_called
    # Defined in the test so nothing else can have called it first.
    klass = Class.new do
      extend T::Sig
      sig { params(a: Integer).returns(Integer) }
      def hi(a) = a
    end

    # Until the first call the method is still the generic wrapper.
    assert_equal(-1, klass.instance_method(:hi).arity)
    assert_equal 1, klass.new.hi(1)
    assert_equal 1, klass.instance_method(:hi).arity
  end

  def test_a_sig_whose_parameter_names_are_wrong_survives_loading
    # A typo in a sig does not fail the boot. It fails the first request that
    # reaches the method, with RuntimeError -- not TypeError.
    klass = Class.new do
      extend T::Sig
      sig { params(quantity: Integer).returns(Integer) }
      def hi(qty) = qty
    end

    err = assert_raises(RuntimeError) { klass.new.hi(1) }
    assert_match(/declaration for `hi` is missing parameter\(s\): qty/, err.message)
  end

  def test_sig_needs_extend_t_sig_and_says_so_bluntly
    err = assert_raises(NoMethodError) do
      Class.new do
        sig { returns(String) }
        def hi = "x"
      end
    end
    assert_match(/undefined method 'sig'/, err.message)
  end

  # ------------------------------------------------------------------
  # 9. void replaces the return value.
  # ------------------------------------------------------------------
  def test_void_hands_the_caller_a_sentinel_not_the_real_value
    result = Inventory::FileLedger.new.record
    refute_equal "recorded", result
    # A private module used as a singleton. Do not reference the constant in
    # real code -- the point is only that it is not your value.
    assert_instance_of Module, result
  end

  # ------------------------------------------------------------------
  # 10. abstract! is enforced at run time.
  # ------------------------------------------------------------------
  def test_an_abstract_class_cannot_be_instantiated
    err = assert_raises(RuntimeError) { Inventory::Ledger.new }
    assert_match(/declared as abstract; it cannot be instantiated/, err.message)
  end

  def test_an_unimplemented_abstract_method_raises_not_implemented_error
    sub = Class.new(Inventory::Ledger)
    err = assert_raises(NotImplementedError) { sub.new.name }
    assert_match(/declared as `abstract`/, err.message)
    assert_equal "file", Inventory::FileLedger.new.name
  end

  # ------------------------------------------------------------------
  # 11. Two ways a check silently is not there.
  # ------------------------------------------------------------------
  def test_checked_never_removes_the_check_for_arguments_and_return_alike
    # Integer in, Integer out, says the sig. This returns "xx".
    assert_equal "xx", Inventory::Meter.double("x")
    assert_equal 4, Inventory::Meter.double(2)
  end

  def test_an_override_without_its_own_sig_is_unchecked
    assert_raises(TypeError) { Inventory::Parent.new.weigh("heavy") }
    assert_equal "heavy", Inventory::Child.new.weigh("heavy")
  end
end
