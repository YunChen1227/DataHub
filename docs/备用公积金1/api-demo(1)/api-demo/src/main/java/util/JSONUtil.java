package util;

import org.codehaus.jackson.map.ObjectMapper;

import java.util.Map;

public class JSONUtil {
    private static final ObjectMapper mapper =new ObjectMapper();

    public static String toJSONString(Object value) throws Exception {
        return mapper.writeValueAsString(value);
    }
    public static Map toMap(String json) throws Exception {
        return mapper.readValue(json, Map.class);
    }
    public static <T> T toBean(String json,Class<T> valueType) throws Exception {
        return mapper.readValue(json, valueType);
    }
}
