# Spring Boot 4.1.0 shallow ETag conditional GET boundary

This contract exercises Spring Framework 7.0.8 from the pinned Spring Boot
4.1.0 line through standalone `MockMvc`. It opens no port and uses no external
service.

`ShallowEtagHeaderFilter` derives an ETag from the completed response body. That
ordering creates the important boundary measured here: matching
`If-None-Match` validators suppress the body and change the response to 304,
but they do not avoid controller work. GET comparison is weak, so both a weak
form of the generated strong tag and a matching weak tag within a
comma-separated list produce the same result. A stale tag returns the original
200 body.

The POM records direct dependency intent only. The manifest is the executable
lock and lists every Maven JAR in the verified classpath explicitly. Resolution
runs from a generated Central-only POM; compilation and the contract run
offline in separate pinned JDK 21 containers.
