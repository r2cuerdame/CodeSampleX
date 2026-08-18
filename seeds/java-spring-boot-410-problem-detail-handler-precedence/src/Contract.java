import java.util.concurrent.atomic.AtomicInteger;

import org.springframework.boot.SpringBootVersion;
import org.springframework.http.HttpStatus;
import org.springframework.http.MediaType;
import org.springframework.http.ProblemDetail;
import org.springframework.test.web.servlet.MockMvc;
import org.springframework.test.web.servlet.MvcResult;
import org.springframework.test.web.servlet.setup.MockMvcBuilders;
import org.springframework.web.bind.annotation.ControllerAdvice;
import org.springframework.web.bind.annotation.ExceptionHandler;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.RestController;

import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.get;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.status;

public final class Contract {
    static final AtomicInteger localCalls = new AtomicInteger();
    static final AtomicInteger adviceCalls = new AtomicInteger();

    static final class WidgetUnavailable extends RuntimeException {
        WidgetUnavailable(String message) {
            super(message);
        }
    }

    @RestController
    static final class LocalHandlerController {
        @GetMapping("/local")
        String local() {
            throw new WidgetUnavailable("widget local-7 is unavailable");
        }

        @ExceptionHandler(WidgetUnavailable.class)
        ProblemDetail handleLocally(WidgetUnavailable error) {
            localCalls.incrementAndGet();
            ProblemDetail detail = ProblemDetail.forStatusAndDetail(
                    HttpStatus.UNPROCESSABLE_CONTENT, error.getMessage());
            detail.setTitle("Local widget failure");
            return detail;
        }
    }

    @RestController
    static final class AdviceOnlyController {
        @GetMapping("/advice")
        String advice() {
            throw new WidgetUnavailable("widget shared-9 is unavailable");
        }
    }

    @ControllerAdvice
    static final class SharedAdvice {
        @ExceptionHandler(WidgetUnavailable.class)
        ProblemDetail handleGlobally(WidgetUnavailable error) {
            adviceCalls.incrementAndGet();
            ProblemDetail detail = ProblemDetail.forStatusAndDetail(
                    HttpStatus.SERVICE_UNAVAILABLE, error.getMessage());
            detail.setTitle("Shared widget failure");
            return detail;
        }
    }

    public static void main(String[] args) throws Exception {
        assertEquals("4.1.0", SpringBootVersion.getVersion(), "Spring Boot implementation version");

        MockMvc mvc = MockMvcBuilders
                .standaloneSetup(new LocalHandlerController(), new AdviceOnlyController())
                .setControllerAdvice(new SharedAdvice())
                .build();

        MvcResult local = mvc.perform(get("/local"))
                .andExpect(status().isUnprocessableContent())
                .andReturn();

        assertEquals(1, localCalls.get(), "local handler invocation count");
        assertEquals(0, adviceCalls.get(), "advice must not handle an exception claimed locally");
        assertProblem(local, 422, "Local widget failure", "widget local-7 is unavailable");

        MvcResult advised = mvc.perform(get("/advice"))
                .andExpect(status().isServiceUnavailable())
                .andReturn();

        assertEquals(1, localCalls.get(), "local handler count after advice-only request");
        assertEquals(1, adviceCalls.get(), "advice handler invocation count");
        assertProblem(advised, 503, "Shared widget failure", "widget shared-9 is unavailable");
    }

    private static void assertProblem(MvcResult result, int status, String title, String detail)
            throws Exception {
        String contentType = result.getResponse().getContentType();
        if (contentType == null || !MediaType.parseMediaType(contentType)
                .isCompatibleWith(MediaType.APPLICATION_PROBLEM_JSON)) {
            throw new AssertionError("content type: got " + contentType
                    + ", expected application/problem+json");
        }

        String body = result.getResponse().getContentAsString();
        assertContains(body, "\"status\":" + status, "serialized ProblemDetail status");
        assertContains(body, "\"title\":\"" + title + "\"", "serialized ProblemDetail title");
        assertContains(body, "\"detail\":\"" + detail + "\"", "serialized ProblemDetail detail");
    }

    private static void assertContains(String actual, String expected, String label) {
        if (!actual.contains(expected)) {
            throw new AssertionError(label + ": got " + actual + ", expected fragment " + expected);
        }
    }

    private static void assertEquals(Object expected, Object actual, String label) {
        if (!expected.equals(actual)) {
            throw new AssertionError(label + ": got " + actual + ", expected " + expected);
        }
    }
}
