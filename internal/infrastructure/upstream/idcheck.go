package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/datahub/relay/internal/domain/model"
)

// 身份证实名核验 (数脉) data.result 业务码 (ShowDoc「返回字段说明」)：
//
//	0 一致（收费）、1 不一致（收费）、2 无记录(预留)
//
// 0/1 都是上游给出的确定核验结论且明标收费 → 归一 001 查得(计费)；2 无记录未标
// 收费 → 归一 999 查无(不计费)。文档标注「预留」，即上游当前可能不返回该值。
const (
	idCheckResultMatch    = 0
	idCheckResultMismatch = 1
	idCheckResultNoRecord = 2
)

// IDCheckConfig holds the 数脉 身份证实名核验 endpoint + 我方凭证。与 rlbd1/rlbd2
// 的人脸比对是同一服务商、同一套鉴权：AppID = 上游 appid（服务商分配的唯一标识），
// AppSecret = 上游 app_security（既用于 sign 的 MD5 拼接，也可作 AES key；
// 首版发明文不加密，故不带 secretMode）。
type IDCheckConfig struct {
	BaseURL   string // https://api.shumaidata.com/v4/id_card/check
	AppID     string
	AppSecret string
}

// IDCheckClient implements port.UpstreamPort for the 身份证实名核验 provider.
// 协议: POST form (application/x-www-form-urlencoded；文档明确「如 body 传参以表单
// 方式提交，不要 json 方式」); sign = md5(appid&timestamp&app_security)，与
// facecompare 同算法(复用 signFaceCompare)。明文传 name/idcard，不传 secretMode。
// 响应 data 富对象序列化经下游 result.range 透出业务字段
// (result/desc/sex/birthday/address)；上游订单号 order_no 不透出，只经 UID/LogID
// 落审计供对账。
type IDCheckClient struct {
	cfg  IDCheckConfig
	http *http.Client
}

// NewIDCheck builds a 身份证实名核验 client.
func NewIDCheck(cfg IDCheckConfig, httpClient *http.Client) *IDCheckClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &IDCheckClient{cfg: cfg, http: httpClient}
}

type idCheckResponse struct {
	Msg     string          `json:"msg"`
	Success bool            `json:"success"`
	Code    int             `json:"code"`
	Data    json.RawMessage `json:"data"`
}

// idCheckData 只取归一化与对账需要的两个字段。Result 用指针：错误返回体的 data
// 是空对象 {}，若用值类型则缺失的 result 会退化成 0 一致（收费）而误计费。
type idCheckData struct {
	Result  *int   `json:"result"`
	OrderNo string `json:"order_no"`
}

// Query performs the signed POST form to 数脉 id_card/check and normalizes the
// response: code=200 且 result 为 0/1 → "001" 查得计费 (Range = data 富对象 JSON
// 字符串，order_no 由 sanitizeRange 剥掉); code=200 且 result=2 → "999" 查无
// (不计费); 其余 (code≠200 的 400/404/500/501/60x/1001，或 result 缺失/超出枚举)
// → error (上游侧异常，触发 re-query/对账，不计费)。
func (c *IDCheckClient) Query(ctx context.Context, req *model.UpstreamRequest) (*model.UpstreamResult, error) {
	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	sign := signFaceCompare(c.cfg.AppID, timestamp, c.cfg.AppSecret)

	form := url.Values{}
	form.Set("appid", c.cfg.AppID)
	form.Set("timestamp", timestamp)
	form.Set("sign", sign)
	form.Set("name", req.Name)
	form.Set("idcard", req.IDCard)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build idcheck request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	slog.Debug("idcheck request",
		"url", c.cfg.BaseURL,
		"appid", c.cfg.AppID,
		"timestamp", timestamp,
		"sign", sign,
		"reqid", req.Reqid,
	)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("idcheck call: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read idcheck body: %w", err)
	}
	slog.Debug("idcheck response", "status", resp.StatusCode, "raw", string(raw))

	var ir idCheckResponse
	if err := json.Unmarshal(raw, &ir); err != nil {
		return nil, fmt.Errorf("decode idcheck body (http %d): %w", resp.StatusCode, err)
	}

	var data idCheckData
	_ = json.Unmarshal(ir.Data, &data)

	if ir.Code != 200 {
		// 400 参数错误 / 404 请求资源不存在 / 500 系统内部错误 / 501 第三方服务异常 /
		// 601 未开通权限 / 602 账号停用 / 603 余额不足 / 604 接口停用 / 606 调用超限 /
		// 1001 其它：均为我方在上游侧的账户/参数/系统问题，视为上游侧错误 (不计费)。
		// 失败也带上游 order_no 落审计供对账追查 (错误返回体的 data 通常为空对象)。
		return nil, busiErrf(ir.Code, ir.Msg, data.OrderNo, "")
	}
	if data.Result == nil {
		// code=200 但没有 result：无法判定是否为可计费结论，按上游侧错误处理。
		return nil, busiErrf(ir.Code, "上游返回 data.result 缺失", data.OrderNo, "")
	}

	switch *data.Result {
	case idCheckResultMatch, idCheckResultMismatch:
		return &model.UpstreamResult{
			Code:  "001",
			Msg:   "成功",
			UID:   data.OrderNo,
			Reqid: req.Reqid,
			LogID: data.OrderNo, // 只有 order_no 一个上游标识，UID/LogID 同填供后台对账
			// order_no 是上游订单号，只进审计(UID/LogID)不进 range——由 sanitizeRange 剥掉。
			Range: sanitizeRange(ir.Data),
		}, nil
	case idCheckResultNoRecord:
		return &model.UpstreamResult{
			Code:  "999",
			Msg:   "没有查询到数据",
			UID:   data.OrderNo,
			Reqid: req.Reqid,
			LogID: data.OrderNo,
		}, nil
	default:
		// 超出文档枚举 (0/1/2) 的取值：不臆断是否收费，按上游侧错误交由对账兜底。
		return nil, busiErrf(ir.Code, fmt.Sprintf("上游返回未知 result=%d", *data.Result), data.OrderNo, "")
	}
}

// Requery: 身份证实名核验暂无幂等对账查询接口 (文档只定义了一个业务接口)。返回
// Reachable=false，记录保持 PENDING 由对账兜底 (与既有上游一致)。
func (c *IDCheckClient) Requery(ctx context.Context, reqid string) (*model.RequeryResult, error) {
	_ = ctx
	_ = reqid
	return &model.RequeryResult{Reachable: false}, nil
}
