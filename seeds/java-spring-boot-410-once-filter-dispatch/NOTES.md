# Spring Boot 4.1.0 OncePerRequestFilter dispatch boundary

This contract exercises Spring Framework 7.0.8 from the pinned Spring Boot
4.1.0 line through standalone `MockMvc`. It opens no port and uses no external
service.

A counting `OncePerRequestFilter` records the real `DispatcherType` each time
its filtering hook runs. The initial async request is a normal REQUEST and is
filtered. MockMvc's follow-up ASYNC dispatch completes the response but is
skipped by the default async policy. A simulated servlet ERROR dispatch includes
both `DispatcherType.ERROR` and the standard error request URI attribute; it
still reaches the mapped controller while the default error policy skips the
filter. A later ordinary request proves the filter remains active for a new
REQUEST.

The POM records direct dependency intent only. The manifest is the executable
lock and lists every Maven JAR in the verified classpath explicitly. Resolution
runs from a generated Central-only POM; compilation and the contract run
offline in separate pinned JDK 21 containers.
