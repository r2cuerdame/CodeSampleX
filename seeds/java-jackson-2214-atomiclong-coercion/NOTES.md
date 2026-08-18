# Jackson 2.21.4 AtomicLong coercion boundary

The patch fixes a narrow but dangerous path: coerced values were parsed as a
`Long` and then narrowed through `intValue()` before constructing the
`AtomicLong`. Numeric strings and floating-point tokens above the signed
32-bit range could therefore become a different number without an error.

The contract covers positive and negative values outside that range, an
integral-token baseline, and a bean property. It also checks that the fix did
not weaken Jackson's coercion controls: disabling float-to-integer coercion or
string-to-integer coercion still rejects the input.

The POM is documentation and dependency metadata only. Verification resolves
its exact public dependency through a sanitized resolver input, then compiles
and runs the contract with plain `javac` and `java` while networking is off.
