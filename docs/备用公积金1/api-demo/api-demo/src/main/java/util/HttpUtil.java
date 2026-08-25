package util;

import org.apache.http.HttpEntity;
import org.apache.http.client.config.RequestConfig;
import org.apache.http.client.methods.CloseableHttpResponse;
import org.apache.http.client.methods.HttpPost;
import org.apache.http.config.Registry;
import org.apache.http.config.RegistryBuilder;
import org.apache.http.conn.socket.ConnectionSocketFactory;
import org.apache.http.conn.socket.PlainConnectionSocketFactory;
import org.apache.http.conn.ssl.NoopHostnameVerifier;
import org.apache.http.conn.ssl.SSLConnectionSocketFactory;
import org.apache.http.entity.BasicHttpEntity;
import org.apache.http.impl.client.CloseableHttpClient;
import org.apache.http.impl.client.HttpClients;
import org.apache.http.impl.conn.PoolingHttpClientConnectionManager;
import org.apache.http.ssl.SSLContexts;
import org.apache.http.util.EntityUtils;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import javax.net.ssl.SSLContext;
import java.io.*;
import java.nio.charset.StandardCharsets;
import java.security.KeyStore;

public class HttpUtil {


    private static Logger logger = LoggerFactory.getLogger(HttpUtil.class);

    private static PoolingHttpClientConnectionManager connectionManager;


    public static String postWithP12(String url, String params,String clientCertPath,String clientCertPass) throws Exception {
        long l1=System.currentTimeMillis();
        if (null==connectionManager){
            HttpUtil.init(clientCertPath,clientCertPass);
        }
        String response = post(url, params, 6000);
        System.out.println("请求地址 :"+url);
        System.out.println("请求参数 :"+params);
        System.out.println("响应参数 :"+response);
        System.out.println("耗时(ms) :"+(System.currentTimeMillis()-l1));
        System.out.println("----------------------------------");
        return response;
    }

    /**
     * 初始化 connection manager.
     *
     * @param clientCertPath
     * @param clientCertPass
     * @throws Exception
     */
    public static void init(String clientCertPath, String clientCertPass) throws Exception {
        System.out.println("初始化连接池中...");
        InputStream ksis = new FileInputStream(new File(clientCertPath));
        KeyStore ks = KeyStore.getInstance("PKCS12");
        ks.load(ksis, clientCertPass.toCharArray());

        SSLContext sslContext = SSLContexts.custom()
                .loadKeyMaterial(ks, clientCertPass.toCharArray())
                .build();

        SSLConnectionSocketFactory sslsf = new SSLConnectionSocketFactory(sslContext,
                new String[] { "TLSv1.2"  },
                null,
                NoopHostnameVerifier.INSTANCE); // not check common name
        Registry<ConnectionSocketFactory> registry = RegistryBuilder
                .<ConnectionSocketFactory> create()
                .register("http", PlainConnectionSocketFactory.INSTANCE)
                .register("https", sslsf).build();
        ksis.close();
        connectionManager = new PoolingHttpClientConnectionManager(registry);
        connectionManager.setDefaultMaxPerRoute(100);
        connectionManager.setMaxTotal(2000);
    }

    /**
     * post请求，url、参数、参数类型、超时时间、授权、字符编码
     */
    public static String post(String url, String params,  int timeout) throws Exception {

        if (null==connectionManager){
            throw new Exception("connectionManager未初始化，请检查是否调用init方法！");
        }

        String result = null;
        CloseableHttpClient httpClient = null;
        CloseableHttpResponse response = null;
        HttpEntity entity = null;
        try {

            httpClient = HttpClients.custom()
                    .setConnectionManager(connectionManager)
                    .setConnectionManagerShared(true)
                    .build();
            // 设置默认http状态参数
            RequestConfig requestConfig = RequestConfig.custom()
                    .setSocketTimeout(timeout)
                    .setConnectTimeout(timeout)
                    .setConnectionRequestTimeout(timeout).build();

            HttpPost httpPost = new HttpPost(url);
            httpPost.setConfig(requestConfig);
            httpPost.setHeader("Content-Type", "application/json;charset=UTF-8");
            if (params != null) {
                BasicHttpEntity requestBody = new BasicHttpEntity();
                byte[] content = params.getBytes(StandardCharsets.UTF_8);
                requestBody.setContent(new ByteArrayInputStream(content));
                requestBody.setContentLength(content.length);
                httpPost.setEntity(requestBody);
            }
            // 执行客户端请求
            response = httpClient.execute(httpPost);
            entity = response.getEntity();
            if (entity != null) {
                result = EntityUtils.toString(entity, StandardCharsets.UTF_8);
            }
        }catch (Exception e) {
            throw new Exception(e.getMessage(), e);
        }finally {
            // 关闭连接
            if (entity != null) {
                EntityUtils.consumeQuietly(entity);
            }
            if (response != null) {
                try {
                    response.close();
                } catch (IOException e) {
                    logger.error("关闭HttpResponse出错，错误信息：" + e.getMessage(), e);
                }
            }
            if (httpClient != null) {
                try {
                    httpClient.close();
                } catch (IOException e) {
                    logger.error("关闭HttpClient出错，错误信息：" + e.getMessage(), e);
                }
            }
        }
        return result;
    }

}
