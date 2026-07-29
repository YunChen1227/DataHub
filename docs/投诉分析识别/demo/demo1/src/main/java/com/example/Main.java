package com.example;

import java.util.HashMap;
import java.util.Map;

import com.alibaba.fastjson.JSONObject;

public class Main {
    public static void main(String[] args) throws Exception{
        String url = "https://api.kfongtech.com/inlet/api";

        String api_key= "";  //用户key
        String api_secret = ""; //用户secret



        String aeskey = Util.MD5(api_secret).toUpperCase().substring(8, 24);

        String aesiv =  Util.MD5(aeskey).toUpperCase().substring(8, 24);

        Map<String, String> predata = new HashMap<String, String>();


  

        predata.put("method", "api***");
   

        predata.put("version", "1.0.0");



        predata.put("mobile", "207DB08A4FFA253A287870EA734DA354");
        predata.put("name", "a492fa9b601f053acfa8672535d03eb1");
        predata.put("idCard", "1efc4abb5dd72b2f3f6d16c15affdb10");


        String predataStr = Util.sortParam(predata);
        String aesParam = AesUtils.encrypt(predataStr,aeskey,aesiv);

        predata.put("apiKey", api_key);
        String sign = Util.signParam(predata, api_secret);

        Map<String,String> reqData = new HashMap<String,String>();

        reqData.put("apiKey", api_key);
        reqData.put("sign", sign);
        reqData.put("param", aesParam);


        String data = JacksonUtils.serialObject(reqData);

        String postResult = HttpRequest.sendPostJson(url, data);

        JSONObject datajson = JSONObject.parseObject(postResult);

        if("0000".equals(datajson.getString("code")))
        {
            String descdata = GZipUtil.ungzipString(datajson.getString("data"));
            System.out.println(descdata);
        }
        System.out.println(postResult);
    }
}