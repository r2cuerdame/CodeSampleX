# Spring Boot 4.1.0 MockMvc content negotiation boundary

This contract exercises Spring Framework 7.0.8 from the pinned Spring Boot
4.1.0 line through standalone `MockMvc`. It opens no port and uses no external
service.

One controller exposes two methods at the same path, separated only by their
`produces` conditions. Exact and q-weighted `Accept` headers demonstrate the
expected selection. The less obvious boundary is the unrestricted wildcard:
it does not create an ambiguous mapping or follow method declaration order in
this stack, but selects `application/json`. An unsupported media type fails at
mapping with HTTP 406, lists both supported representations, and invokes
neither method.

The POM records direct dependency intent only. The manifest is the executable
lock and lists every Maven JAR in the verified classpath explicitly. Resolution
runs from a generated Central-only POM; compilation and the contract run
offline in separate pinned JDK 21 containers.
