# Spring Boot 4.1.0 CORS preflight policy boundary

This contract exercises Spring Framework 7.0.8 from the pinned Spring Boot
4.1.0 line through standalone `MockMvc`. It opens no port and uses no external
service.

A `CorsFilter` policy permits a single origin, PUT, and `X-Token` header for the
controller path. The valid preflight is handled entirely by Spring's CORS
processing: it returns the exact allow headers and never enters the controller.
Changing only the origin, requested method, or requested header independently
yields HTTP 403 without a partial allow-origin response, also without controller
invocation. A final real PUT demonstrates that the allowed endpoint itself runs
and keeps the CORS response header.

The POM records direct dependency intent only. The manifest is the executable
lock and lists every Maven JAR in the verified classpath explicitly. Resolution
runs from a generated Central-only POM; compilation and the contract run
offline in separate pinned JDK 21 containers.
