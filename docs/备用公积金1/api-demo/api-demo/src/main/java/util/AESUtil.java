package util;


import org.apache.commons.codec.binary.Base64;
import javax.crypto.Cipher;
import javax.crypto.KeyGenerator;
import javax.crypto.spec.SecretKeySpec;
import java.security.SecureRandom;
import java.util.Arrays;

/**
 * @author mu
 * @date 2019年08月29日
 */
public class AESUtil {




    public static String encrpt(String toBeEncrpyt,String key) throws Exception {


            KeyGenerator kgen = KeyGenerator.getInstance("AES");
            Cipher cipher = Cipher.getInstance("AES/ECB/NoPadding");
            kgen.init(128, new SecureRandom(key.getBytes()));
            cipher.init(Cipher.ENCRYPT_MODE, new SecretKeySpec(key.getBytes(), "AES"));
            byte[] decryptBytes = cipher.doFinal(appendZero(toBeEncrpyt).getBytes());
            String aa= Base64.encodeBase64String(decryptBytes);
            return aa.replace("\n", "");

    }
    public static String decrypt(String toBeDecrypt,String key) throws Exception
    {


            KeyGenerator kgen = KeyGenerator.getInstance("AES");
            Cipher cipher = Cipher.getInstance("AES/ECB/NoPadding");
            kgen.init(128, new SecureRandom(key.getBytes()));
            cipher.init(Cipher.DECRYPT_MODE, new SecretKeySpec(key.getBytes(), "AES"));
            byte[] decryptBytes = cipher.doFinal(Base64.decodeBase64(toBeDecrypt));
            return new String(decryptBytes).trim();
    }

    private static String appendZero(String oriString)
    {
        byte[] ss = oriString.getBytes();
        int pad_len = ss.length % 16 ;
        pad_len=pad_len==0?16:16-pad_len;

        byte[] bs = new byte[ss.length+pad_len];
        Arrays.fill(bs, (byte) (0x00));

        System.arraycopy(ss, 0, bs, 0, ss.length);
        return new String(bs);

    }


    public static void main(String[] args) {

    }


}
