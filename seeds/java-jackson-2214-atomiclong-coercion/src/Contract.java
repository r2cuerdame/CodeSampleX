import com.fasterxml.jackson.databind.DeserializationFeature;
import com.fasterxml.jackson.databind.JsonMappingException;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.cfg.CoercionAction;
import com.fasterxml.jackson.databind.cfg.CoercionInputShape;
import com.fasterxml.jackson.databind.json.JsonMapper;
import com.fasterxml.jackson.databind.type.LogicalType;

import java.util.concurrent.atomic.AtomicLong;

public final class Contract {
    public static final class Payload {
        public AtomicLong value;
    }

    @FunctionalInterface
    private interface MappingCall {
        void run() throws Exception;
    }

    public static void main(String[] args) throws Exception {
        ObjectMapper mapper = new ObjectMapper();

        assertValue(
                mapper.readValue("\"4294967296\"", AtomicLong.class),
                4_294_967_296L,
                "numeric string above the 32-bit range");
        assertValue(
                mapper.readValue("2147483648.75", AtomicLong.class),
                2_147_483_648L,
                "floating-point coercion above the 32-bit range");
        assertValue(
                mapper.readValue("\"-2147483649\"", AtomicLong.class),
                -2_147_483_649L,
                "negative numeric string below the 32-bit range");
        assertValue(
                mapper.readValue("9223372036854775806", AtomicLong.class),
                9_223_372_036_854_775_806L,
                "integral-token baseline near Long.MAX_VALUE");

        Payload payload = mapper.readValue("{\"value\":\"4294967296\"}", Payload.class);
        assertValue(payload.value, 4_294_967_296L, "AtomicLong bean property string coercion");

        ObjectMapper rejectFloat = JsonMapper.builder()
                .disable(DeserializationFeature.ACCEPT_FLOAT_AS_INT)
                .build();
        expectMappingFailure(
                () -> rejectFloat.readValue("2147483648.75", AtomicLong.class),
                "disabled floating-point coercion");

        ObjectMapper rejectString = JsonMapper.builder()
                .withCoercionConfig(LogicalType.Integer, config ->
                        config.setCoercion(CoercionInputShape.String, CoercionAction.Fail))
                .build();
        expectMappingFailure(
                () -> rejectString.readValue("\"4294967296\"", AtomicLong.class),
                "disabled numeric-string coercion");
    }

    private static void assertValue(AtomicLong actual, long expected, String label) {
        if (actual == null || actual.longValue() != expected) {
            throw new AssertionError(label + ": got " + actual + ", expected " + expected);
        }
    }

    private static void expectMappingFailure(MappingCall call, String label) throws Exception {
        try {
            call.run();
        } catch (JsonMappingException expected) {
            return;
        }
        throw new AssertionError(label + ": expected JsonMappingException");
    }
}
