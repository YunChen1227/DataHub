package util;

import org.apache.commons.codec.digest.DigestUtils;

import java.util.*;


/**
 * 签名工具类
 *
 */
public class SignUtil {

    /**
     * 根据map key升序排序
     *
     * @param sortedParams
     * @return
     */
    public static String getSign(Map<String, Object> sortedParams, String signkey) throws Exception {

        StringBuilder signSrc = new StringBuilder();
        List<String> keys = new ArrayList<>(sortedParams.keySet());
        //排序
        Collections.sort(keys);
        for (String key : keys) {
            Object value = sortedParams.get(key);
            //不参与签名的几种情况：参数不存在(null)，参数为空（""），参数值为不存在（null），参数为sign
            if (key != null && !"".equals(key) && value != null&&!"".equals(value) && !"sign".equals(key)) {
                signSrc.append(key).append("=").append(value).append("&");
            }
        }
//        System.out.println("待签名字符串:"+signSrc + "key="+signkey);
        String sign = DigestUtils.md5Hex(signSrc + "key="+signkey);
        return sign;

    }

    public static void main(String[] args) throws Exception {
        Map value=null;
        System.out.println(value==null);
    }

}
