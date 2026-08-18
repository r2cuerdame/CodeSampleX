import java.util.concurrent.atomic.AtomicInteger;

import org.springframework.boot.SpringBootVersion;
import org.springframework.http.MediaType;
import org.springframework.validation.BindingResult;
import org.springframework.web.bind.WebDataBinder;
import org.springframework.web.bind.annotation.InitBinder;
import org.springframework.web.bind.annotation.ModelAttribute;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RestController;
import org.springframework.test.web.servlet.MockMvc;
import org.springframework.test.web.servlet.setup.MockMvcBuilders;

import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.post;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.content;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.status;

public final class Contract {
    static final AtomicInteger handlerCalls = new AtomicInteger();

    static final class AccountForm {
        private String displayName;
        private String role = "user";

        public String getDisplayName() {
            return this.displayName;
        }

        public void setDisplayName(String displayName) {
            this.displayName = displayName;
        }

        public String getRole() {
            return this.role;
        }

        public void setRole(String role) {
            this.role = role;
        }
    }

    @RestController
    static final class AccountController {
        @InitBinder("account")
        void restrictAccountFields(WebDataBinder binder) {
            binder.setDisallowedFields("role");
        }

        @PostMapping(path = "/account", produces = MediaType.TEXT_PLAIN_VALUE)
        String update(
                @ModelAttribute("account") AccountForm account,
                BindingResult binding) {
            handlerCalls.incrementAndGet();
            return "name=" + account.getDisplayName()
                    + ";role=" + account.getRole()
                    + ";errors=" + binding.getErrorCount()
                    + ";suppressed=" + String.join(",", binding.getSuppressedFields());
        }
    }

    public static void main(String[] args) throws Exception {
        assertEquals("4.1.0", SpringBootVersion.getVersion(), "Spring Boot implementation version");

        MockMvc mvc = MockMvcBuilders.standaloneSetup(new AccountController()).build();

        mvc.perform(post("/account")
                        .param("displayName", "Ada")
                        .param("role", "admin")
                        .param("typo", "ignored"))
                .andExpect(status().isOk())
                .andExpect(content().string(
                        "name=Ada;role=user;errors=0;suppressed=role"));

        assertEquals(1, handlerCalls.get(), "handler invocation count");
    }

    private static void assertEquals(Object expected, Object actual, String label) {
        if (!expected.equals(actual)) {
            throw new AssertionError(label + ": got " + actual + ", expected " + expected);
        }
    }
}
