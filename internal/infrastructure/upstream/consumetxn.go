package upstream

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/datahub/relay/internal/domain/model"
)

// ConsumeTxnProcode 是消费交易特征接口的固定产品标识符 (data-bean 接口文档：
// 本接口 procode 取值 fk3002)。可经 config apiKey 覆盖（默认取此值）。
const ConsumeTxnProcode = "fk3002"

// ConsumeTxnConfig holds the 消费交易特征 (data-bean) endpoint + 我方凭证。
// SceneID = 上游 sceneid（区分业务场景，控制台分配），AppKey = 上游 appkey
// （拼入 "&appkey=" 后参与 MD5 签名）；Procode 为固定产品码（默认 fk3002）。
type ConsumeTxnConfig struct {
	BaseURL string // https://api.data-bean.cn
	SceneID string
	AppKey  string
	Procode string
}

// ConsumeTxnClient implements port.UpstreamPort for the 消费交易特征 provider.
// 协议: POST JSON (application/json); 公共参数 procode/sceneid/reqtime/nonce/sign +
// 私有参数 params{name,idcard,mobile,authlet}; sign = MD5(过滤空值后按 key ASCII
// 升序拼接的 "k=v&k=v..."（procode/sceneid/reqtime/nonce 与 params 扁平化后一起
// 参与）+ "&appkey=" + appkey) 小写 hex。响应 data.resultdata 富对象原样序列化经
// 下游 result.range 透出；data.result "0"查得(计费)/"1"查无(不计费)。
type ConsumeTxnClient struct {
	cfg  ConsumeTxnConfig
	http *http.Client
}

// NewConsumeTxn builds a 消费交易特征 client (procode 空时默认 fk3002)。
func NewConsumeTxn(cfg ConsumeTxnConfig, httpClient *http.Client) *ConsumeTxnClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if cfg.Procode == "" {
		cfg.Procode = ConsumeTxnProcode
	}
	return &ConsumeTxnClient{cfg: cfg, http: httpClient}
}

type consumeTxnResponse struct {
	Code  flexString `json:"code"`
	Msg   string     `json:"msg"`
	Reqno string     `json:"reqno"`
	Data  struct {
		ResultData json.RawMessage `json:"resultdata"`
		Result     flexString      `json:"result"`
	} `json:"data"`
}

// flexString accepts upstream JSON fields as string or number (data-bean 实际返回 code/result 为数字)。
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*f = ""
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*f = flexString(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err == nil {
		*f = flexString(n.String())
		return nil
	}
	return fmt.Errorf("flexString: invalid JSON %s", string(b))
}

// Query performs the signed JSON POST to 消费交易特征 and normalizes the response:
// code=0 且 result=0 → "001" 查得(计费)，Range = data.resultdata 富对象 JSON 字符串；
// code=0 且 result=1 → "999" 查无(不计费)；code≠0（或 result 非 0/1）→ error
// (账户/参数/系统等上游侧异常，不计费，触发 re-query/对账)。
func (c *ConsumeTxnClient) Query(ctx context.Context, req *model.UpstreamRequest) (*model.UpstreamResult, error) {
	reqtime := strconv.FormatInt(time.Now().UnixMilli(), 10)
	nonce := c.cfg.SceneID + randAlnum(8)

	// 私有业务参数（非空才放，字段名对齐上游 params 契约）。
	params := map[string]string{}
	if req.Name != "" {
		params["name"] = req.Name
	}
	if req.IDCard != "" {
		params["idcard"] = req.IDCard
	}
	if req.Mobile != "" {
		params["mobile"] = req.Mobile
	}
	if req.Authlet != "" {
		params["authlet"] = req.Authlet
	}

	// 参与签名的字段：公共参数 procode/sceneid/reqtime/nonce 与 params 扁平化后一起
	// 排序（sign/url 不参与，空值已在拼接前过滤）。
	signFields := map[string]string{
		"procode": c.cfg.Procode,
		"sceneid": c.cfg.SceneID,
		"reqtime": reqtime,
		"nonce":   nonce,
	}
	for k, v := range params {
		signFields[k] = v
	}
	sign := signConsumeTxn(signFields, c.cfg.AppKey)

	body := map[string]any{
		"procode": c.cfg.Procode,
		"sceneid": c.cfg.SceneID,
		"reqtime": reqtime,
		"nonce":   nonce,
		"sign":    sign,
		"params":  params,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal consumetxn request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build consumetxn request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	slog.Debug("consumetxn request",
		"url", c.cfg.BaseURL,
		"procode", c.cfg.Procode,
		"sceneid", c.cfg.SceneID,
		"reqtime", reqtime,
		"nonce", nonce,
		"sign", sign,
		"reqid", req.Reqid,
	)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("consumetxn call: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read consumetxn body: %w", err)
	}
	slog.Debug("consumetxn response", "status", resp.StatusCode, "raw", string(raw))

	var cr consumeTxnResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		return nil, fmt.Errorf("decode consumetxn body: %w", err)
	}
	if string(cr.Code) != "0" {
		// 非 0 应答码：我方在上游侧的账户/参数/系统问题，视为上游侧错误 (不计费)，
		// 由 orchestrator 走 re-query/对账兜底。失败也带上游 reqno(请求号) 落审计供追查。
		return nil, busiErr(string(cr.Code), cr.Msg, cr.Reqno, cr.Reqno)
	}

	// reqno 是上游唯一请求号，UID/LogID 同填——成功/查无都要能在后台「上游logId」对账。
	uid := cr.Reqno
	switch string(cr.Data.Result) {
	case "0":
		return &model.UpstreamResult{
			Code:  "001",
			Msg:   "成功",
			UID:   uid,
			Reqid: req.Reqid,
			LogID: cr.Reqno,
			Range: compactJSON(cr.Data.ResultData),
		}, nil
	case "1":
		return &model.UpstreamResult{
			Code:  "999",
			Msg:   "未查得",
			UID:   uid,
			Reqid: req.Reqid,
			LogID: cr.Reqno,
		}, nil
	default:
		// result 非 0/1：语义未定义，按上游侧异常处理（不计费）。
		return nil, busiErr(string(cr.Code), fmt.Sprintf("未知 result=%q msg=%s", cr.Data.Result, cr.Msg), cr.Reqno, cr.Reqno)
	}
}

// Requery: 消费交易特征暂无幂等对账查询接口，未联调前返回 Reachable=false，
// 记录保持 PENDING 由对账兜底 (与既有上游一致)。
func (c *ConsumeTxnClient) Requery(ctx context.Context, reqid string) (*model.RequeryResult, error) {
	_ = ctx
	_ = reqid
	return &model.RequeryResult{Reachable: false}, nil
}

// signConsumeTxn 计算 sign = MD5(过滤空值后按 key ASCII 升序拼接的 "k=v&k=v..."
// + "&appkey=" + appkey) 小写 hex（对齐 data-bean sign 算法：url/sign 不参与、
// 空值过滤、params 值提取出来参与排序）。
func signConsumeTxn(fields map[string]string, appKey string) string {
	keys := make([]string, 0, len(fields))
	for k, v := range fields {
		if v != "" { // 空值不参与签名
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var sb strings.Builder
	for i, k := range keys {
		if i > 0 {
			sb.WriteString("&")
		}
		sb.WriteString(k)
		sb.WriteString("=")
		sb.WriteString(fields[k])
	}
	sb.WriteString("&appkey=")
	sb.WriteString(appKey)
	sum := md5.Sum([]byte(sb.String()))
	return hex.EncodeToString(sum[:])
}

// randAlnum 生成 n 位 [A-Za-z0-9] 随机串（nonce 防重放用，sceneid+随机 8 位）。
func randAlnum(n int) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		// 极少发生；退化为时间戳派生，保证长度不影响功能（仅防重放强度下降）。
		fallback := strconv.FormatInt(time.Now().UnixNano(), 36)
		for i := range buf {
			buf[i] = fallback[i%len(fallback)]
		}
		return string(buf)
	}
	for i := range buf {
		buf[i] = alphabet[int(buf[i])%len(alphabet)]
	}
	return string(buf)
}
