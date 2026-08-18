import java.util.concurrent.atomic.AtomicInteger;

import org.springframework.boot.SpringBootVersion;
import org.springframework.http.HttpHeaders;
import org.springframework.http.MediaType;
import org.springframework.test.web.servlet.MockMvc;
import org.springframework.test.web.servlet.MvcResult;
import org.springframework.test.web.servlet.setup.MockMvcBuilders;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.RestController;
import org.springframework.web.filter.ShallowEtagHeaderFilter;

import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.get;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.content;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.header;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.status;

public final class Contract {
    static final AtomicInteger handlerCalls = new AtomicInteger();

    @RestController
    static final class VersionController {
        @GetMapping(path = "/version", produces = MediaType.TEXT_PLAIN_VALUE)
        String version() {
            handlerCalls.incrementAndGet();
            return "version-1";
        }
    }

    public static void main(String[] args) throws Exception {
        assertEquals("4.1.0", SpringBootVersion.getVersion(), "Spring Boot implementation version");

        MockMvc mvc = MockMvcBuilders.standaloneSetup(new VersionController())
                .addFilters(new ShallowEtagHeaderFilter())
                .build();

        MvcResult first = mvc.perform(get("/version"))
                .andExpect(status().isOk())
                .andExpect(content().string("version-1"))
                .andReturn();

        String etag = first.getResponse().getHeader(HttpHeaders.ETAG);
        assertTrue(etag != null && etag.matches("\"0[0-9a-f]{32}\""),
                "default filter emits one strong quoted MD5 ETag");
        assertEquals(1, handlerCalls.get(), "handler calls after initial GET");

        mvc.perform(get("/version").header(HttpHeaders.IF_NONE_MATCH, etag))
                .andExpect(status().isNotModified())
                .andExpect(header().string(HttpHeaders.ETAG, etag))
                .andExpect(content().string(""));
        assertEquals(2, handlerCalls.get(), "matching strong validator still runs handler");

        mvc.perform(get("/version").header(HttpHeaders.IF_NONE_MATCH, "W/" + etag))
                .andExpect(status().isNotModified())
                .andExpect(header().string(HttpHeaders.ETAG, etag))
                .andExpect(content().string(""));
        assertEquals(3, handlerCalls.get(), "matching weak validator still runs handler");

        mvc.perform(get("/version").header(
                        HttpHeaders.IF_NONE_MATCH,
                        "\"stale\", W/" + etag))
                .andExpect(status().isNotModified())
                .andExpect(header().string(HttpHeaders.ETAG, etag))
                .andExpect(content().string(""));
        assertEquals(4, handlerCalls.get(), "matching tag in a validator list still runs handler");

        mvc.perform(get("/version").header(HttpHeaders.IF_NONE_MATCH, "\"stale\""))
                .andExpect(status().isOk())
                .andExpect(header().string(HttpHeaders.ETAG, etag))
                .andExpect(content().string("version-1"));
        assertEquals(5, handlerCalls.get(), "stale validator runs handler and returns body");
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
