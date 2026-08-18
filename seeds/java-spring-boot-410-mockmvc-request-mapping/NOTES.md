# Spring Boot 4.1.0 MockMvc request mapping boundary

This contract exercises the Spring Framework 7.0.8 web stack shipped with the
pinned Spring Boot 4.1.0 line. It uses standalone `MockMvc`, so the controller
is dispatched through Spring MVC without opening a port or contacting an
external service.

The route has three independent gates: the class-level path, the method-level
path, and the `view=full` request-mapping condition. A subtle Spring 7 boundary
appears here: a wrong path is a 404, but the right path with an unsatisfied
request-mapping parameter condition is resolved as
`UnsatisfiedServletRequestParameterException` and returns 400. Once the route
matches, `limit` is a required integer request parameter; missing and invalid
values are distinct resolved exceptions that both return 400.

The POM documents only the direct dependency intent. Verification does not
execute it: the manifest lists the complete exact Maven JAR closure, which is
resolved from a generated Central-only project. Compilation and the MockMvc
contract then run offline in separate pinned JDK 21 containers.
