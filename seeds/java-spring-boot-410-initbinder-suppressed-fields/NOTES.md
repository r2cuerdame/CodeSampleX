# Spring Boot 4.1.0 InitBinder suppressed-field boundary

This contract exercises Spring Framework 7.0.8 from the pinned Spring Boot
4.1.0 line through standalone `MockMvc`. It opens no port and uses no external
service.

One form request contains three kinds of input. The normal `displayName`
property binds. The known `role` property is explicitly disallowed, so it does
not overwrite the model default and appears in `BindingResult`'s suppressed
field list without becoming a binding error. The unknown `typo` property takes
a third path under the default binder: it is silently ignored and does not
appear as either an error or a suppressed field. The controller therefore
still returns HTTP 200.

The POM records direct dependency intent only. The manifest is the executable
lock and lists every Maven JAR in the verified classpath explicitly. Resolution
runs from a generated Central-only POM; compilation and the contract run
offline in separate pinned JDK 21 containers.
