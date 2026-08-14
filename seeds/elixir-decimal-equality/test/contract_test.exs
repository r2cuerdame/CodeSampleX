defmodule CsxDecimalTest do
  use ExUnit.Case

  # Decimal fixes float arithmetic and then hands you a subtler problem:
  # two Decimals that are numerically equal are different structs, because
  # the scale is part of the value. Every construct that uses Elixir's
  # built-in equality — ==, in, Enum.uniq, pattern matching, sorting — is
  # therefore wrong on Decimals, and wrong quietly.

  test "the arithmetic is the easy part" do
    refute 0.1 + 0.2 == 0.3
    assert Decimal.add(Decimal.new("0.1"), Decimal.new("0.2")) |> Decimal.to_string() == "0.3"
  end

  test "Decimal.new refuses a float on purpose" do
    # Taking the float would import the error Decimal exists to avoid, so
    # the conversion has to be asked for by name.
    #
    # apply/3 keeps this an assertion about runtime behaviour. Written as a
    # literal, Elixir 1.20's type checker rejects it at compile time — the
    # same answer, arriving earlier, which is worth knowing on its own.
    assert_raise FunctionClauseError, fn -> apply(Decimal, :new, [0.1]) end
    assert Decimal.from_float(0.1) |> Decimal.to_string() == "0.1"
    assert Decimal.from_float(0.1 + 0.2) |> Decimal.to_string() == "0.30000000000000004"
  end

  test "== compares the struct, so equal values are not equal" do
    a = Decimal.new("1.0")
    b = Decimal.new("1.00")
    refute a == b
    assert Decimal.equal?(a, b)
    assert Decimal.compare(a, b) == :eq
  end

  test "everything built on == inherits the bug" do
    a = Decimal.new("1.0")
    b = Decimal.new("1.00")
    assert length(Enum.uniq([a, b])) == 2
    refute b in [a]
  end

  test "sorting needs the module, not the default term order" do
    values = [Decimal.new("2"), Decimal.new("1.0")]
    # Structs sort by their fields, and coef comes before exp, so 2 sorts
    # before 1.0 — a wrong order that looks right on plenty of inputs.
    assert Enum.map(Enum.sort(values), &Decimal.to_string/1) == ["2", "1.0"]
    assert Enum.map(Enum.sort(values, Decimal), &Decimal.to_string/1) == ["1.0", "2"]
  end

  test "normalize is how you get a canonical form" do
    assert Decimal.normalize(Decimal.new("1.00")) |> Decimal.to_string() == "1"
    assert Decimal.normalize(Decimal.new("1.0")) == Decimal.normalize(Decimal.new("1.00"))
  end
end
