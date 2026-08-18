import java.io.IOException;
import java.util.List;
import java.util.concurrent.Callable;
import java.util.concurrent.CopyOnWriteArrayList;
import java.util.concurrent.atomic.AtomicInteger;

import jakarta.servlet.DispatcherType;
import jakarta.servlet.FilterChain;
import jakarta.servlet.RequestDispatcher;
import jakarta.servlet.ServletException;
import jakarta.servlet.http.HttpServletRequest;
import jakarta.servlet.http.HttpServletResponse;
import org.springframework.boot.SpringBootVersion;
import org.springframework.http.MediaType;
import org.springframework.test.web.servlet.MockMvc;
import org.springframework.test.web.servlet.MvcResult;
import org.springframework.test.web.servlet.setup.MockMvcBuilders;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.RestController;
import org.springframework.web.filter.OncePerRequestFilter;

import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.asyncDispatch;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.get;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.content;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.header;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.request;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.status;

public final class Contract {
    static final AtomicInteger controllerCalls = new AtomicInteger();

    static final class CountingFilter extends OncePerRequestFilter {
        final List<DispatcherType> seen = new CopyOnWriteArrayList<>();

        @Override
        protected void doFilterInternal(
                HttpServletRequest request,
                HttpServletResponse response,
                FilterChain filterChain) throws ServletException, IOException {
            DispatcherType type = request.getDispatcherType();
            this.seen.add(type);
            response.addHeader("X-Filtered-Dispatch", type.name());
            filterChain.doFilter(request, response);
        }
    }

    @RestController
    static final class DispatchController {
        @GetMapping(path = "/async", produces = MediaType.TEXT_PLAIN_VALUE)
        Callable<String> async() {
            controllerCalls.incrementAndGet();
            return () -> "async";
        }

        @GetMapping(path = "/sync", produces = MediaType.TEXT_PLAIN_VALUE)
        String sync() {
            controllerCalls.incrementAndGet();
            return "sync";
        }
    }

    public static void main(String[] args) throws Exception {
        assertEquals("4.1.0", SpringBootVersion.getVersion(), "Spring Boot implementation version");

        CountingFilter filter = new CountingFilter();
        MockMvc mvc = MockMvcBuilders.standaloneSetup(new DispatchController())
                .addFilters(filter)
                .build();

        MvcResult initial = mvc.perform(get("/async"))
                .andExpect(request().asyncStarted())
                .andReturn();
        assertEquals(List.of(DispatcherType.REQUEST), filter.seen,
                "filter dispatches after initial async request");
        assertEquals(1, controllerCalls.get(), "controller calls after initial async request");

        mvc.perform(asyncDispatch(initial))
                .andExpect(status().isOk())
                .andExpect(content().string("async"));
        assertEquals(List.of(DispatcherType.REQUEST), filter.seen,
                "default filter skips ASYNC redispatch");
        assertEquals(1, controllerCalls.get(), "async redispatch does not reinvoke controller method");

        mvc.perform(get("/sync").with(request -> {
                    request.setDispatcherType(DispatcherType.ERROR);
                    request.setAttribute(RequestDispatcher.ERROR_REQUEST_URI, "/failed");
                    return request;
                }))
                .andExpect(status().isOk())
                .andExpect(header().doesNotExist("X-Filtered-Dispatch"))
                .andExpect(content().string("sync"));
        assertEquals(List.of(DispatcherType.REQUEST), filter.seen,
                "default filter skips ERROR dispatch with servlet error attribute");
        assertEquals(2, controllerCalls.get(), "ERROR-typed request still reaches mapped controller");

        mvc.perform(get("/sync"))
                .andExpect(status().isOk())
                .andExpect(header().string("X-Filtered-Dispatch", "REQUEST"))
                .andExpect(content().string("sync"));
        assertEquals(List.of(DispatcherType.REQUEST, DispatcherType.REQUEST), filter.seen,
                "later ordinary request is filtered independently");
        assertEquals(3, controllerCalls.get(), "final controller invocation count");
    }

    private static void assertEquals(Object expected, Object actual, String label) {
        if (!expected.equals(actual)) {
            throw new AssertionError(label + ": got " + actual + ", expected " + expected);
        }
    }
}
