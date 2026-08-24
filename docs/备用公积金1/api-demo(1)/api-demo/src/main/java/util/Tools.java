package util;

import java.util.Map;

public class Tools {
    /**
     * 方法：组装生成params参数 判断是否选择AES加密 是：返回加密字符串 否：返回Map对象
     */
    public static Object genParams(Map<String, Object> paramsMap, String merchantKey, Boolean isEncrpt) throws Exception {

        //判断是否需要AES加密
        if (isEncrpt) {
            String finalParams = JSONUtil.toJSONString(paramsMap);
            finalParams = AESUtil.encrpt(finalParams, merchantKey);
            return finalParams;
        }else {
            //不需要加密 则直接返回Map对象 这样在序列化的时候双引号前不会带有转义斜杆\
            return paramsMap;
        }


    }
}
