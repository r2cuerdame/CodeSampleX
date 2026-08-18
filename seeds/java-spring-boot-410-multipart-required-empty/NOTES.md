# Spring Boot 4.1.0 required multipart empty-file boundary

This contract exercises Spring Framework 7.0.8 from the pinned Spring Boot
4.1.0 line through standalone `MockMvc`. It opens no port and uses no external
service.

The boundary is presence versus content. A required `MultipartFile` rejects an
absent part with `MissingServletRequestPartException` before controller
invocation. Once the named part exists, however, required binding succeeds even
when its body is zero bytes and `MultipartFile.isEmpty()` is true. Neither an
empty original filename nor a supplied filename and content type changes that
result; content validation remains application work.

The POM records direct dependency intent only. The manifest is the executable
lock and lists every Maven JAR in the verified classpath explicitly. Resolution
runs from a generated Central-only POM; compilation and the contract run
offline in separate pinned JDK 21 containers.
