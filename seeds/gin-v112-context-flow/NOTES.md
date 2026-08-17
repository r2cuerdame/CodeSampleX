# Gin context flow

This contract answers the five exact `gin.Context.*` coordinates on the public
Wanted board. It uses `httptest` only and never opens a network listener.

The important boundary is that `ShouldBindJSON` reports an error without
choosing a response, while `AbortWithStatusJSON` stops later handlers but does
not act like a Go `return` in the current handler.
