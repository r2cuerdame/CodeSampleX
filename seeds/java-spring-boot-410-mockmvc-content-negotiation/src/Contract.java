import java.util.Map;
import java.util.concurrent.atomic.AtomicInteger;

import org.springframework.boot.SpringBootVersion;
import org.springframework.http.HttpHeaders;
import org.springframework.http.MediaType;
import org.springframework.test.web.servlet.MockMvc;
import org.springframework.test.web.servlet.MvcResult;
import org.springframework.test.web.servlet.setup.MockMvcBuilders;
import org.springframework.web.HttpMediaTypeNotAcceptableException;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.RestController;

import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.get;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.content;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.status;

public final class Contract {
    static final AtomicInteger jsonCalls = new AtomicInteger();
    static final AtomicInteger textCalls = new AtomicInteger();

    @RestController
    static final class FormatController {
        @GetMapping(path = "/format", produces = MediaType.TEXT_PLAIN_VALUE)
        String text() {
            textCalls.incrementAndGet();
            return "plain";
        }

        @GetMapping(path = "/format", produces = MediaType.APPLICATION_JSON_VALUE)
        Map<String, String> json() {
            jsonCalls.incrementAndGet();
            return Map.of("format", "json");
        }
    }

    public static void main(String[] args) throws Exception {
        assertEquals("4.1.0", SpringBootVersion.getVersion(), "Spring Boot implementation version");

        MockMvc mvc = MockMvcBuilders.standaloneSetup(new FormatController()).build();

        mvc.perform(get("/format").accept(MediaType.APPLICATION_JSON))
                .andExpect(status().isOk())
                .andExpect(content().contentTypeCompatibleWith(MediaType.APPLICATION_JSON))
                .andExpect(content().string("{\"format\":\"json\"}"));

        mvc.perform(get("/format").accept(MediaType.TEXT_PLAIN))
                .andExpect(status().isOk())
                .andExpect(content().contentTypeCompatibleWith(MediaType.TEXT_PLAIN))
                .andExpect(content().string("plain"));

        mvc.perform(get("/format").header(
                        HttpHeaders.ACCEPT,
                        "text/plain;q=0.4, application/json;q=0.9"))
                .andExpect(status().isOk())
                .andExpect(content().contentTypeCompatibleWith(MediaType.APPLICATION_JSON))
                .andExpect(content().string("{\"format\":\"json\"}"));

        mvc.perform(get("/format").accept(MediaType.ALL))
                .andExpect(status().isOk())
                .andExpect(content().contentTypeCompatibleWith(MediaType.APPLICATION_JSON))
                .andExpect(content().string("{\"format\":\"json\"}"));

        assertEquals(3, jsonCalls.get(), "JSON handler calls before unsupported request");
        assertEquals(1, textCalls.get(), "text handler calls before unsupported request");

        MvcResult unsupported = mvc.perform(get("/format").accept(MediaType.APPLICATION_XML))
                .andExpect(status().isNotAcceptable())
                .andReturn();

        Exception resolved = unsupported.getResolvedException();
        if (!(resolved instanceof HttpMediaTypeNotAcceptableException notAcceptable)) {
            throw new AssertionError("resolved exception: got " + resolved
                    + ", expected HttpMediaTypeNotAcceptableException");
        }
        assertTrue(notAcceptable.getSupportedMediaTypes().contains(MediaType.APPLICATION_JSON),
                "406 advertises application/json support");
        assertTrue(notAcceptable.getSupportedMediaTypes().contains(MediaType.TEXT_PLAIN),
                "406 advertises text/plain support");
        assertEquals(3, jsonCalls.get(), "JSON handler calls after unsupported request");
        assertEquals(1, textCalls.get(), "text handler calls after unsupported request");
    }

    private static void assertTrue(boolean condition, String label) {
        if (!condition) {
            throw new AssertionError(label);
        }
    }

    private static void assertEquals(Object expected, Object actual, String label) {
        if (!expected.equals(actual)) {
            throw new AssertionError(label + ": got " + actual + ", expected " + expected);
        }
    }
}
