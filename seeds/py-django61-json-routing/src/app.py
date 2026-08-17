from django.http import HttpResponse, JsonResponse
from django.urls import path


class MarkerMiddleware:
    def __init__(self, get_response):
        self.get_response = get_response

    def __call__(self, request):
        request.passed_middleware = True
        response = self.get_response(request)
        response["X-Middleware"] = "applied"
        return response


def item_view(request, item_id):
    return JsonResponse(
        {
            "item_id": item_id,
            "item_id_type": type(item_id).__name__,
            "middleware": getattr(request, "passed_middleware", False),
        }
    )


def list_view(request):
    return JsonResponse(["first", "second"], safe=False)


def compact_view(request):
    return JsonResponse(
        {"z": 1, "a": "한글"},
        json_dumps_params={
            "ensure_ascii": False,
            "sort_keys": True,
            "separators": (",", ":"),
        },
    )


def text_view(request):
    return HttpResponse("plain", content_type="text/plain")


urlpatterns = [
    path("items/<int:item_id>/", item_view, name="item-detail"),
    path("items/", list_view, name="item-list"),
    path("compact/", compact_view, name="compact-json"),
    path("text/", text_view, name="plain-text"),
]
