import org.springframework.boot.SpringBootVersion;
import org.springframework.test.web.servlet.MockMvc;
import org.springframework.test.web.servlet.setup.MockMvcBuilders;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.bind.annotation.RestController;
import org.springframework.web.bind.MissingServletRequestParameterException;
import org.springframework.web.bind.UnsatisfiedServletRequestParameterException;
import org.springframework.web.method.annotation.MethodArgumentTypeMismatchException;

import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.get;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.content;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.status;

public final class Contract {
    @RestController
    @RequestMapping(path = "/api/widgets")
    static final class WidgetController {
        @GetMapping(path = "/{id}", params = "view=full")
        String show(
                @PathVariable("id") String id,
                @RequestParam("limit") int limit) {
            return "widget=" + id + ";limit=" + limit;
        }
    }

    public static void main(String[] args) throws Exception {
        assertEquals("4.1.0", SpringBootVersion.getVersion(), "Spring Boot implementation version");

        MockMvc mvc = MockMvcBuilders.standaloneSetup(new WidgetController()).build();

        mvc.perform(get("/api/widgets/alpha")
                        .param("view", "full")
                        .param("limit", "3"))
                .andExpect(status().isOk())
                .andExpect(content().string("widget=alpha;limit=3"));

        mvc.perform(get("/api/widgets/alpha")
                        .param("limit", "3"))
                .andExpect(status().isBadRequest())
                .andExpect(result -> assertResolved(
                        result.getResolvedException(),
                        UnsatisfiedServletRequestParameterException.class));

        mvc.perform(get("/api/gadgets/alpha")
                        .param("view", "full")
                        .param("limit", "3"))
                .andExpect(status().isNotFound());

        mvc.perform(get("/api/widgets/alpha")
                        .param("view", "full"))
                .andExpect(status().isBadRequest())
                .andExpect(result -> assertResolved(
                        result.getResolvedException(),
                        MissingServletRequestParameterException.class));

        mvc.perform(get("/api/widgets/alpha")
                        .param("view", "full")
                        .param("limit", "not-an-integer"))
                .andExpect(status().isBadRequest())
                .andExpect(result -> assertResolved(
                        result.getResolvedException(),
                        MethodArgumentTypeMismatchException.class));
    }

    private static void assertEquals(String expected, String actual, String label) {
        if (!expected.equals(actual)) {
            throw new AssertionError(label + ": got " + actual + ", expected " + expected);
        }
    }

    private static void assertResolved(Exception actual, Class<? extends Exception> expected) {
        if (!expected.isInstance(actual)) {
            throw new AssertionError(
                    "resolved exception: got " + actual + ", expected " + expected.getName());
        }
    }
}
