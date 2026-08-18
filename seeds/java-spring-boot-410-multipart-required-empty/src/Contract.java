import java.util.concurrent.atomic.AtomicInteger;

import org.springframework.boot.SpringBootVersion;
import org.springframework.http.MediaType;
import org.springframework.mock.web.MockMultipartFile;
import org.springframework.test.web.servlet.MockMvc;
import org.springframework.test.web.servlet.setup.MockMvcBuilders;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.bind.annotation.RestController;
import org.springframework.web.multipart.MultipartFile;
import org.springframework.web.multipart.support.MissingServletRequestPartException;

import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.multipart;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.content;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.status;

public final class Contract {
    static final AtomicInteger handlerCalls = new AtomicInteger();

    @RestController
    static final class UploadController {
        @PostMapping(
                path = "/upload",
                consumes = MediaType.MULTIPART_FORM_DATA_VALUE,
                produces = MediaType.TEXT_PLAIN_VALUE)
        String upload(@RequestParam("file") MultipartFile file) {
            handlerCalls.incrementAndGet();
            return "empty=" + file.isEmpty()
                    + ";name=" + file.getOriginalFilename()
                    + ";type=" + file.getContentType()
                    + ";size=" + file.getSize();
        }
    }

    public static void main(String[] args) throws Exception {
        assertEquals("4.1.0", SpringBootVersion.getVersion(), "Spring Boot implementation version");

        MockMvc mvc = MockMvcBuilders.standaloneSetup(new UploadController()).build();

        mvc.perform(multipart("/upload"))
                .andExpect(status().isBadRequest())
                .andExpect(result -> assertResolved(
                        result.getResolvedException(),
                        MissingServletRequestPartException.class));
        assertEquals(0, handlerCalls.get(), "handler calls after missing required part");

        MockMultipartFile unnamedEmpty = new MockMultipartFile("file", new byte[0]);
        mvc.perform(multipart("/upload").file(unnamedEmpty))
                .andExpect(status().isOk())
                .andExpect(content().string("empty=true;name=;type=null;size=0"));
        assertEquals(1, handlerCalls.get(), "handler calls after unnamed empty part");

        MockMultipartFile namedEmpty = new MockMultipartFile(
                "file", "empty.txt", MediaType.TEXT_PLAIN_VALUE, new byte[0]);
        mvc.perform(multipart("/upload").file(namedEmpty))
                .andExpect(status().isOk())
                .andExpect(content().string("empty=true;name=empty.txt;type=text/plain;size=0"));
        assertEquals(2, handlerCalls.get(), "handler calls after named zero-length part");
    }

    private static void assertResolved(Exception actual, Class<? extends Exception> expected) {
        if (!expected.isInstance(actual)) {
            throw new AssertionError(
                    "resolved exception: got " + actual + ", expected " + expected.getName());
        }
    }

    private static void assertEquals(Object expected, Object actual, String label) {
        if (!expected.equals(actual)) {
            throw new AssertionError(label + ": got " + actual + ", expected " + expected);
        }
    }
}
