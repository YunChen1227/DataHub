package com.example;





import java.io.ByteArrayInputStream;
import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.nio.charset.Charset;
import java.nio.charset.StandardCharsets;
import org.apache.commons.codec.binary.Base64;
import java.util.zip.GZIPInputStream;
import java.util.zip.GZIPOutputStream;

/**
 * 
 * 对字符串进行加解密和加解压
 * @author wujh
 *
 */
@SuppressWarnings("restriction")

public class GZipUtil {
	
	/**
	 * 将字符串压缩后Base64
	 * @param primStr 待加压加密函数
	 * @return
	 */
	public static String gzipString(String primStr) {
		if (primStr == null || primStr.length() == 0) {
			return primStr;
		}
		ByteArrayOutputStream out = null;  
		GZIPOutputStream gout = null;  
		try{  
			out = new ByteArrayOutputStream();  
			gout = new GZIPOutputStream(out);
			gout.write(primStr.getBytes(Charset.forName("Utf-8")));  
			gout.flush();
		} catch (IOException e) {
			System.out.println("对字符串进行加压加密操作失败："+e.fillInStackTrace());
			return null;
		} finally {
			if (gout != null) {
				try {
					gout.close();
				} catch (IOException e) {
					System.out.println("对字符串进行加压加密操作，关闭gzip操作流失败："+e.fillInStackTrace());
				}
			}
		}
		return Base64.encodeBase64String(out.toByteArray());
	}
	
	/**
	 * 将压缩并Base64后的字符串进行解密解压
	 * @param compressedStr 待解密解压字符串
	 * @return
	 */
	public static final String ungzipString(String compressedStr) {
		if (compressedStr == null) {
			return null;
		}
		ByteArrayOutputStream out = null;
		ByteArrayInputStream in = null;
		GZIPInputStream gin = null;
		String decompressed = null;
		try {
			byte[] compressed =Base64.decodeBase64(compressedStr);
			out = new ByteArrayOutputStream();
			in = new ByteArrayInputStream(compressed);
			gin = new GZIPInputStream(in);
			byte[] buffer = new byte[1024];
			int offset = -1;
			while((offset = gin.read(buffer)) != -1) {
				out.write(buffer, 0, offset);
			}
			decompressed = out.toString("Utf-8");
		} catch (IOException e) {
			System.out.println("对字符串进行解密解压操作失败："+e.fillInStackTrace());
			
			decompressed = null;
		} finally {
			if (gin != null) {
				try {
					gin.close();
				} catch (IOException e) {
					System.out.println("对字符串进行解密解压操作，关闭压缩流失败："+e.fillInStackTrace());
				}
			}
			if (in != null) {
				try {
					in.close();
				} catch (IOException e) {
					System.out.println("对字符串进行解密解压操作，关闭输入流失败："+e.fillInStackTrace());
				}
			}
			if (out != null) {
				try {
					out.close();
				} catch (IOException e) {
					System.out.println("对字符串进行解密解压操作，关闭输出流失败："+e.fillInStackTrace());
				}
			}
		}
		return decompressed;
	}

	/**
	 * 将字符串压缩并Base64加密，字符编码为utf-8
	 * @param str 待处理字符串
	 * @return 压缩加密后的字符串
	 * @throws IOException 压缩过程中发生的压缩格式不匹配等IO异常
	 */
	public static String gzipEncode(String str) throws IOException {
		return gzipEncode(str, StandardCharsets.UTF_8);
	}

	/**
	 * 将字符串压缩并Base64加密
	 * @param str 待处理字符串
	 * @return 压缩加密后的字符串
	 * @throws IOException 压缩过程中发生的压缩格式不匹配等IO异常
	 */
	public static String gzipEncode(String str, Charset charset) throws IOException {
		return encoderByBase64(gzipCompress(str, charset));
	}

	/**
	 * 输入字节码，返回Base64加密后的字符串
	 * @param bytes 字节码
	 * @return Base64加密后的字符串
	 */
	public static String encoderByBase64(byte[] bytes) {
		return Base64.encodeBase64String(bytes);
	}

	/**
	 * gzip压缩
	 * @param str 待压缩字符串
	 * @param charset 字符编码
	 * @return gzip压缩的字节码
	 * @throws IOException 压缩过程中发生的压缩格式不匹配等IO异常
	 */
	public static byte[] gzipCompress(String str, Charset charset) throws IOException {
		ByteArrayOutputStream bytesStream = new ByteArrayOutputStream();
		try (GZIPOutputStream gzip = new GZIPOutputStream(bytesStream)) {
			gzip.write(str.getBytes(charset));
			gzip.flush();
		}
		return bytesStream.toByteArray();
    }

	public static void main(String[] args) {
		String data = "H4sIAAAAAAAAAKtWKijKT0pUsjLQM7GoBQAuo7RtDgAAAA==";
		String decodedata = ungzipString(data);
		try {
			String resd = gzipEncode(decodedata);
			System.out.print(resd);
		
		} catch (Exception e) {
			
			System.out.print(e);
		}

	}
}
	