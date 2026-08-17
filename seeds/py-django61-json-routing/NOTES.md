# Django 6.1 JsonResponse and in-process routing

This sample separates two Django testing surfaces that are easy to conflate.
`RequestFactory` creates a request object only. Calling a view with that object
does not resolve `django.urls.path`, apply path converters, run middleware, or
add the test client's response helpers. `Client` runs the in-process request
stack and does all of those things without starting a server or database.

The contract also fixes the `JsonResponse` boundaries for Django 6.1:

- dictionaries are accepted by default and encoded as `application/json`;
- lists require `safe=False`;
- `json_dumps_params` controls ordering, spacing, and Unicode escaping;
- the `.json()` helper added to client responses refuses a non-JSON content
  type instead of parsing arbitrary response bytes.

The dependency closure is fully pinned and hash checked for reproducible
resolution before the contract runs offline.
