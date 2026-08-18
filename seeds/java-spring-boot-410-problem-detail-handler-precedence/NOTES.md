# Spring Boot 4.1.0 ProblemDetail handler precedence

This contract exercises Spring Framework 7.0.8 from the pinned Spring Boot
4.1.0 line through standalone `MockMvc`. It opens no port and uses no external
service.

Two controllers throw the same exception. One declares a local
`@ExceptionHandler`; the other relies on a matching `@ControllerAdvice` method.
Invocation counters prove that the local handler wins before advice lookup, not
merely that the final status happens to match. The fallback request then proves
that the advice is active. Both paths return distinct `ProblemDetail` objects,
whose status, title, detail, and `application/problem+json` media type are
checked on the serialized HTTP responses.

The POM records direct dependency intent only. The manifest is the executable
lock and lists every Maven JAR in the verified classpath explicitly. Resolution
runs from a generated Central-only POM; compilation and the contract run
offline in separate pinned JDK 21 containers.
