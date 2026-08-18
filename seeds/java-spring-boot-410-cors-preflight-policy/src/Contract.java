import java.util.List;
import java.util.concurrent.atomic.AtomicInteger;

import org.springframework.boot.SpringBootVersion;
import org.springframework.http.HttpHeaders;
import org.springframework.http.MediaType;
import org.springframework.web.bind.annotation.PutMapping;
import org.springframework.web.bind.annotation.RestController;
import org.springframework.web.cors.CorsConfiguration;
import org.springframework.web.cors.UrlBasedCorsConfigurationSource;
import org.springframework.web.filter.CorsFilter;
import org.springframework.test.web.servlet.MockMvc;
import org.springframework.test.web.servlet.setup.MockMvcBuilders;

import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.options;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.put;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.content;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.header;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.status;

public final class Contract {
    static final String ALLOWED_ORIGIN = "https://client.example.com";
    static final AtomicInteger handlerCalls = new AtomicInteger();

    @RestController
    static final class ItemController {
        @PutMapping(path = "/items/7", produces = MediaType.TEXT_PLAIN_VALUE)
        String update() {
            handlerCalls.incrementAndGet();
            return "updated";
        }
    }

    public static void main(String[] args) throws Exception {
        assertEquals("4.1.0", SpringBootVersion.getVersion(), "Spring Boot implementation version");

        CorsConfiguration policy = new CorsConfiguration();
        policy.setAllowedOrigins(List.of(ALLOWED_ORIGIN));
        policy.setAllowedMethods(List.of("PUT"));
        policy.setAllowedHeaders(List.of("X-Token"));
        policy.setMaxAge(600L);

        UrlBasedCorsConfigurationSource source = new UrlBasedCorsConfigurationSource();
        source.registerCorsConfiguration("/items/**", policy);

        MockMvc mvc = MockMvcBuilders.standaloneSetup(new ItemController())
                .addFilters(new CorsFilter(source))
                .build();

        mvc.perform(options("/items/7")
                        .header(HttpHeaders.ORIGIN, ALLOWED_ORIGIN)
                        .header(HttpHeaders.ACCESS_CONTROL_REQUEST_METHOD, "PUT")
                        .header(HttpHeaders.ACCESS_CONTROL_REQUEST_HEADERS, "X-Token"))
                .andExpect(status().isOk())
                .andExpect(header().string(HttpHeaders.ACCESS_CONTROL_ALLOW_ORIGIN, ALLOWED_ORIGIN))
                .andExpect(header().string(HttpHeaders.ACCESS_CONTROL_ALLOW_METHODS, "PUT"))
                .andExpect(header().string(HttpHeaders.ACCESS_CONTROL_ALLOW_HEADERS, "X-Token"))
                .andExpect(header().string(HttpHeaders.ACCESS_CONTROL_MAX_AGE, "600"))
                .andExpect(content().string(""));
        assertEquals(0, handlerCalls.get(), "handler calls after allowed preflight");

        mvc.perform(options("/items/7")
                        .header(HttpHeaders.ORIGIN, "https://attacker.example.net")
                        .header(HttpHeaders.ACCESS_CONTROL_REQUEST_METHOD, "PUT")
                        .header(HttpHeaders.ACCESS_CONTROL_REQUEST_HEADERS, "X-Token"))
                .andExpect(status().isForbidden())
                .andExpect(header().doesNotExist(HttpHeaders.ACCESS_CONTROL_ALLOW_ORIGIN));

        mvc.perform(options("/items/7")
                        .header(HttpHeaders.ORIGIN, ALLOWED_ORIGIN)
                        .header(HttpHeaders.ACCESS_CONTROL_REQUEST_METHOD, "DELETE")
                        .header(HttpHeaders.ACCESS_CONTROL_REQUEST_HEADERS, "X-Token"))
                .andExpect(status().isForbidden())
                .andExpect(header().doesNotExist(HttpHeaders.ACCESS_CONTROL_ALLOW_ORIGIN));

        mvc.perform(options("/items/7")
                        .header(HttpHeaders.ORIGIN, ALLOWED_ORIGIN)
                        .header(HttpHeaders.ACCESS_CONTROL_REQUEST_METHOD, "PUT")
                        .header(HttpHeaders.ACCESS_CONTROL_REQUEST_HEADERS, "X-Admin"))
                .andExpect(status().isForbidden())
                .andExpect(header().doesNotExist(HttpHeaders.ACCESS_CONTROL_ALLOW_ORIGIN));
        assertEquals(0, handlerCalls.get(), "handler calls after all preflight requests");

        mvc.perform(put("/items/7")
                        .header(HttpHeaders.ORIGIN, ALLOWED_ORIGIN)
                        .header("X-Token", "secret"))
                .andExpect(status().isOk())
                .andExpect(header().string(HttpHeaders.ACCESS_CONTROL_ALLOW_ORIGIN, ALLOWED_ORIGIN))
                .andExpect(content().string("updated"));
        assertEquals(1, handlerCalls.get(), "handler calls after actual allowed PUT");
    }

    private static void assertEquals(Object expected, Object actual, String label) {
        if (!expected.equals(actual)) {
            throw new AssertionError(label + ": got " + actual + ", expected " + expected);
        }
    }
}
