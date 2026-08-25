import util.*;

import java.util.*;

public class TestDemo {
    /**
     * 入口main函数 商户复制util的工具类、引入pom.xml的依赖包和调整main的代码即可轻松实现对接
     */
    public static void main(String[] args) throws Exception {
        //商户配置    TODO【通过线下获取商户配置参数】
        String clientCertPath = "[请填写.p12证书路径]";
        String clientCertPass = "[请填写.p12证书密码]";
        String merchantID     = "[请填写商户号]";
        String merchantKey    = "[请填写商户密钥]";
        String apiAddress     = "[请填写api接口地址]";


        for (int i = 0; i < 1; i++) {//第一次连接池初始化会有一些额外耗时 生产上可把此循环控制语句去掉

            //系统参数
            Map<String, Object> reqMap = new HashMap<String, Object>();
            reqMap.put("merchant_id", merchantID);
            reqMap.put("timestamp", System.currentTimeMillis());
            reqMap.put("dsorderid", System.currentTimeMillis()  + UUID.randomUUID().toString().replace("-", "").substring(0, 11));

            //业务参数  TODO【根据接口文档,填写对应的业务参数 如name、mobile】
            Map<String, Object> paramsMap = new HashMap<>();
            paramsMap.put("name", "张三");
            paramsMap.put("mobile", "4409346387279842");


            //组转业务参数
            reqMap.put("params", Tools.genParams(paramsMap, merchantKey, false));//生产使用https防止中间人攻击,因此AES为可选项 为了使用更安全的方案可选AES 但会产生些许额外的耗时
            //加签
            reqMap.put("sign", SignUtil.getSign(reqMap, merchantKey));
            //发起https请求
            String response = HttpUtil.postWithP12(apiAddress, JSONUtil.toJSONString(reqMap), clientCertPath, clientCertPass);

            //可对response进行解析获取值来使用
        }
    }


}
