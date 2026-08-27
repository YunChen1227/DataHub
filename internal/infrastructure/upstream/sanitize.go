package upstream

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

// upstreamOnlyKeys 是**禁止**出现在下游 body.result.range 里的字段名（归一形态：
// 小写、去掉 _ - 空格）。口径：range 只透出「业务数据」，一切能指向上游身份或用于
// 上游侧对账的标识一律剥掉——上游订单号/流水号/请求号(trade no 一类)、上游会话与
// 追踪号、上游侧凭证与签名、上游账户/产品/场景编号。这些标识仍会经
// model.UpstreamResult 的 UID/LogID 落进审计（后台「上游uid/上游logId」两列），
// 运营照旧能向上游对账；只是不再随响应体外泄给下游。
//
// 新增上游时若上游响应带了本表未覆盖的标识类字段，在此补一条即可——sanitizeRange
// 会对所有序列化透出的路由统一生效，不必逐个客户端改。
var upstreamOnlyKeys = map[string]bool{
	// 订单号 / 流水号 / 交易号
	"orderno": true, "orderid": true, "ordersn": true, "ordercode": true,
	"tradeno": true, "tradeid": true, "transactionid": true, "transactionno": true,
	"serialno": true, "serialnumber": true, "seqno": true, "seqid": true,
	"batchno": true, "flowno": true, "flowid": true, "bizno": true, "bizid": true,
	"outbizno": true, "businessno": true, "resporder": true,
	// 请求号 / 日志号 / 追踪号
	"reqno": true, "reqid": true, "requestno": true, "requestid": true,
	"logid": true, "logno": true, "traceid": true, "spanid": true,
	"msgid": true, "messageid": true, "sessionid": true, "uuid": true, "uid": true,
	"token": true,
	// 上游侧凭证 / 签名 / 账户与产品编号（正常不会出现在结果体里，防御性剥离）
	"appid": true, "appkey": true, "apikey": true, "appsecret": true,
	"secret": true, "signature": true, "sign": true,
	"accountid": true, "merchantno": true, "merchantid": true,
	"prodid": true, "procode": true, "sceneid": true,
}

// isUpstreamOnlyKey 判断一个上游字段名是否属于禁止透出的上游标识类字段。
// 归一化后比较，故 order_no / orderNo / OrderNo / order-no 均命中同一条规则。
func isUpstreamOnlyKey(key string) bool {
	norm := strings.ToLower(key)
	norm = strings.NewReplacer("_", "", "-", "", " ", "").Replace(norm)
	return upstreamOnlyKeys[norm]
}

// sanitizeRange 把上游业务对象压紧成 result.range 透出的 JSON 字符串，并**递归**剥掉
// upstreamOnlyKeys 里的上游标识字段（对象与数组任意层级、任意嵌套）。字段顺序与数字
// 字面量原样保留（用 token 流重写而非 map 往返），故对下游的可见变化只有「少了那些
// 被剥掉的键」。
//
// 空输入返回空串。**非法 JSON 一律返回空串（fail closed）**——解析不了就无法确认里面
// 没有上游标识，此时宁可不透出，也不能把整段原文倒给下游。
func sanitizeRange(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // 保住 932.26 这类字面量，不被转成 float64 再格式化成科学计数法
	var buf bytes.Buffer
	if err := copySanitized(dec, &buf); err != nil {
		slog.Warn("result.range 上游对象无法解析，按 fail closed 置空", "err", err)
		return ""
	}
	// 尾部还有内容说明不是单个完整 JSON 值（如两个对象首尾相接）——同样 fail closed，
	// 否则会静默丢掉后半段，而后半段里可能正藏着上游标识。
	if dec.More() {
		slog.Warn("result.range 上游对象尾部有多余内容，按 fail closed 置空")
		return ""
	}
	return buf.String()
}

// copySanitized 逐 token 复制一个 JSON 值到 out，跳过命中 isUpstreamOnlyKey 的对象键。
func copySanitized(dec *json.Decoder, out *bytes.Buffer) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, isDelim := tok.(json.Delim)
	if !isDelim {
		// 标量：string / json.Number / bool / nil。json.Marshal 对 json.Number
		// 原样输出字面量，对 nil 输出 null。
		enc, err := json.Marshal(tok)
		if err != nil {
			return err
		}
		out.Write(enc)
		return nil
	}

	switch delim {
	case '{':
		out.WriteByte('{')
		first := true
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := keyTok.(string)
			if !ok {
				return fmt.Errorf("对象键不是字符串: %v", keyTok)
			}
			if isUpstreamOnlyKey(key) {
				if err := skipJSONValue(dec); err != nil {
					return err
				}
				continue
			}
			if !first {
				out.WriteByte(',')
			}
			first = false
			enc, err := json.Marshal(key)
			if err != nil {
				return err
			}
			out.Write(enc)
			out.WriteByte(':')
			if err := copySanitized(dec, out); err != nil {
				return err
			}
		}
		if _, err := dec.Token(); err != nil { // 消费 '}'
			return err
		}
		out.WriteByte('}')
		return nil
	case '[':
		out.WriteByte('[')
		first := true
		for dec.More() {
			if !first {
				out.WriteByte(',')
			}
			first = false
			if err := copySanitized(dec, out); err != nil {
				return err
			}
		}
		if _, err := dec.Token(); err != nil { // 消费 ']'
			return err
		}
		out.WriteByte(']')
		return nil
	}
	return fmt.Errorf("非预期的 JSON 分隔符 %v", delim)
}

// skipJSONValue 丢弃 decoder 当前位置的一个完整 JSON 值（标量或任意深度的对象/数组）。
func skipJSONValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, isDelim := tok.(json.Delim)
	if !isDelim {
		return nil
	}
	if delim == '}' || delim == ']' {
		return fmt.Errorf("非预期的闭合分隔符 %v", delim)
	}
	depth := 1
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		if d, ok := tok.(json.Delim); ok {
			switch d {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
		}
	}
	return nil
}
