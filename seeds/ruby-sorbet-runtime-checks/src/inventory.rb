# frozen_string_literal: true

require "sorbet-runtime"

# What sorbet-runtime actually enforces at run time.
#
# The trap this file exists for: the SAME type expression, T::Array[Integer],
# is enforced to a different depth depending on where you wrote it.
#
#   in a sig            -> only the outer class is checked. ["a"] gets through.
#   as a T::Struct prop -> every element is checked. ["a"] raises.
#   through from_hash   -> not checked at all. ["a"] gets through again.
#
# sorbet-runtime validates a sig parameter with T::Types::Base#valid?, which
# for a typed collection asks `obj.is_a?(Array)` and stops, because walking
# every element on every call would be an unbounded cost per call. T::Props
# (what T::Struct is built from) validates with #recursively_valid?, which
# does walk. Nothing in the sig syntax tells you which one you are getting.
module Inventory
  # An order as a T::Struct. The element type here is real: the constructor
  # and the generated setter both walk item_ids.
  class Order < T::Struct
    const :id, Integer
    prop :item_ids, T::Array[Integer]

    # default: [] does NOT alias one array across every instance. T::Props
    # copies the default per instance, so the classic Ruby `def initialize(xs
    # = [])` aliasing bug does not apply here. `factory: -> { [] }` is for
    # defaults that are expensive or must not be copied, not for safety.
    prop :tags, T::Array[String], default: []
  end

  class Gate
    extend T::Sig

    # The obvious wrong version. A reader sees T::Array[Integer] and concludes
    # that whatever comes out of this method is an array of Integers. At run
    # time this admits ["a", "b"] unchanged: sorbet-runtime checked that the
    # argument is an Array and nothing more. Only `srb tc` catches the
    # elements, and `srb tc` is not running in production.
    sig { params(item_ids: T::Array[Integer]).returns(T::Array[Integer]) }
    def self.admit(item_ids)
      item_ids
    end

    # The version that holds wherever it is written. recursively_valid? is the
    # deep check sorbet-runtime already ships and uses internally for props;
    # it is the same type object, just asked the other question.
    sig { params(item_ids: T::Array[Integer]).returns(T::Array[Integer]) }
    def self.admit_deeply(item_ids)
      unless T::Array[Integer].recursively_valid?(item_ids)
        raise TypeError, "item_ids: expected T::Array[Integer], got #{item_ids.inspect}"
      end

      item_ids
    end
  end

  # checked(:never) removes the runtime check entirely, arguments and return
  # value alike. The sig still documents intent and still drives srb tc, so a
  # method carrying it looks exactly as safe as one that is checked.
  class Meter
    extend T::Sig

    sig { params(n: Integer).returns(Integer).checked(:never) }
    def self.double(n)
      n * 2
    end
  end

  # A sig is not inherited. Parent#weigh is checked; Child#weigh replaces the
  # method with a plain untyped one and the check disappears with it. There is
  # no runtime complaint about the missing sig.
  class Parent
    extend T::Sig

    sig { params(kilos: Integer).returns(Integer) }
    def weigh(kilos)
      kilos
    end
  end

  class Child < Parent
    def weigh(kilos)
      kilos
    end
  end

  class Ledger
    extend T::Sig
    extend T::Helpers
    abstract!

    sig { abstract.returns(String) }
    def name; end
  end

  class FileLedger < Ledger
    extend T::Sig

    sig { override.returns(String) }
    def name
      "file"
    end

    # sig { void } does not mean "I am ignoring the return value". It REPLACES
    # it: the caller receives a private sentinel module, so assigning the
    # result of a void method gives you the sentinel and never "recorded".
    sig { void }
    def record
      "recorded"
    end
  end
end
