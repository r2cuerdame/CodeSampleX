package gincontextcontract

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

type createInput struct {
	Name string `json:"name" binding:"required"`
}

func newRouter(t *testing.T, trace *[]string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()

	router.Use(func(c *gin.Context) {
		c.Set("request-id", "request-7")
		c.Next()
	})

	router.POST("/items",
		func(c *gin.Context) {
			requestID, exists := c.Get("request-id")
			if !exists || requestID != "request-7" {
				t.Fatalf("Context.Get request-id = (%v, %v)", requestID, exists)
			}
			if missing, exists := c.Get("missing"); exists || missing != nil {
				t.Fatalf("Context.Get missing = (%v, %v)", missing, exists)
			}

			var input createInput
			if err := c.ShouldBindJSON(&input); err != nil {
				*trace = append(*trace, "bind-error")
				if c.Writer.Written() {
					t.Fatal("ShouldBindJSON wrote a response before the handler chose one")
				}
				c.AbortWithStatusJSON(http.StatusUnprocessableEntity, gin.H{
					"error":      "invalid input",
					"request_id": requestID,
				})
				if !c.IsAborted() {
					t.Fatal("Context.IsAborted returned false after AbortWithStatusJSON")
				}
				*trace = append(*trace, "after-abort")
				return
			}

			c.Set("item-name", input.Name)
			*trace = append(*trace, "validated")
		},
		func(c *gin.Context) {
			*trace = append(*trace, "downstream")
			name, exists := c.Get("item-name")
			if !exists {
				t.Fatal("Context.Get did not return the value set by the prior handler")
			}
			c.JSON(http.StatusCreated, gin.H{"name": name})
		},
	)

	return router
}

func postJSON(t *testing.T, router http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/items", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func decodeJSON(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
		t.Fatalf("decode response %q: %v", response.Body.String(), err)
	}
	return value
}

func TestInvalidJSONUsesExplicitAbortResponseAndSkipsDownstream(t *testing.T) {
	trace := []string{}
	response := postJSON(t, newRouter(t, &trace), `{}`)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnprocessableEntity)
	}
	wantBody := map[string]any{"error": "invalid input", "request_id": "request-7"}
	if got := decodeJSON(t, response); !reflect.DeepEqual(got, wantBody) {
		t.Fatalf("body = %#v, want %#v", got, wantBody)
	}
	if want := []string{"bind-error", "after-abort"}; !reflect.DeepEqual(trace, want) {
		t.Fatalf("trace = %#v, want %#v", trace, want)
	}
}

func TestValidJSONCarriesSetValuesToTheNextHandler(t *testing.T) {
	trace := []string{}
	response := postJSON(t, newRouter(t, &trace), `{"name":"widget"}`)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusCreated)
	}
	if got := decodeJSON(t, response); !reflect.DeepEqual(got, map[string]any{"name": "widget"}) {
		t.Fatalf("body = %#v", got)
	}
	if want := []string{"validated", "downstream"}; !reflect.DeepEqual(trace, want) {
		t.Fatalf("trace = %#v, want %#v", trace, want)
	}
}
