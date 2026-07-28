package upstream

import (
	"context"
	"crypto/md5"
	"encoding/hex"
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

// 人脸身份证比对一所 (数脉) incorrect 业务码 (ShowDoc 接口文档 incorrect 字段详解)。
// 「是否收费」列为「是」的均为上游给出的确定收费结论 → 归一 001 查得(计费)；
// 「否」的为照片/数据/系统类问题 → 视为上游侧错误(不计费, 走复查/对账兜底)。
var faceComparePaidCodes = map[int]bool{
	100: true, // 比对成功
	101: true, // 身份证号码姓名不一致
	103: true, // 身份核验成功，数据非法
	109: true, // 身份核验成功，库中无照片
	110: true, // 身份核验成功，特征提取失败
	111: true, // 身份核验成功，检测到多于一张人脸
	112: true, // 身份核验成功，图片不合法
}

// FaceCompareConfig holds the 数脉 人脸身份证比对一所 endpoint + 我方凭证。
// AppID = 上游 appid（服务商分配的唯一标识），AppSecret = 上游 app_security
// （既用于 sign 的 MD5 拼接，也可作为 AES 加密 key；首版发明文不加密）。
type FaceCompareConfig struct {
	BaseURL   string // https://api.shumaidata.com/v4/face_id_card/yisuo/compare
	AppID     string
	AppSecret string
}

// FaceCompareClient implements port.UpstreamPort for the 人脸身份证比对一所 provider.
// 协议: POST form (application/x-www-form-urlencoded); sign = md5(appid&timestamp&
// app_security); 明文传 name/idcard + image(base64) 或 url (二选一)。响应 data 富对象
// (order_no/score/incorrect/sex/birthday/address) 原样序列化经下游 result.range 透出。
type FaceCompareClient struct {
	cfg  FaceCompareConfig
	http *http.Client
}

// NewFaceCompare builds a 人脸身份证比对一所 client.
func NewFaceCompare(cfg FaceCompareConfig, httpClient *http.Client) *FaceCompareClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &FaceCompareClient{cfg: cfg, http: httpClient}
}

type faceCompareResponse struct {
	Msg     string          `json:"msg"`
	Success bool            `json:"success"`
	Code    int             `json:"code"`
	Data    json.RawMessage `json:"data"`
}

type faceCompareData struct {
	OrderNo   string `json:"order_no"`
	Incorrect int    `json:"incorrect"`
}

// Query performs the signed POST form to 数脉 face_id_card/yisuo/compare and
// normalizes the response: code=200 且 incorrect 为收费码 → "001" 查得
// (Range = data 富对象 JSON 字符串); 其余 (不收费码 / code≠200) → error
// (上游侧异常, 触发 re-query/对账, 不计费)。本上游无「查无」概念, 不产生 "999"。
func (c *FaceCompareClient) Query(ctx context.Context, req *model.UpstreamRequest) (*model.UpstreamResult, error) {
	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	sign := signFaceCompare(c.cfg.AppID, timestamp, c.cfg.AppSecret)

	form := url.Values{}
	form.Set("appid", c.cfg.AppID)
	form.Set("timestamp", timestamp)
	form.Set("sign", sign)
	form.Set("name", req.Name)
	form.Set("idcard", req.IDCard)
	// image、url 二选一（parse.ParseFace 已保证至少一个非空；image 优先）。
	if req.Image != "" {
		form.Set("image", req.Image)
	} else if req.URL != "" {
		form.Set("url", req.URL)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build facecompare request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	slog.Debug("facecompare request",
		"url", c.cfg.BaseURL,
		"appid", c.cfg.AppID,
		"timestamp", timestamp,
		"sign", sign,
		"hasImage", req.Image != "",
		"hasURL", req.URL != "",
		"reqid", req.Reqid,
	)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("facecompare call: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read facecompare body: %w", err)
	}
	slog.Debug("facecompare response", "status", resp.StatusCode, "raw", string(raw))

	var fr faceCompareResponse
	if err := json.Unmarshal(raw, &fr); err != nil {
		return nil, fmt.Errorf("decode facecompare body: %w", err)
	}
	if fr.Code != 200 {
		// 400 参数错误 / 500 系统错误 / 501 第三方异常 / 60x 账户与权限问题：
		// 均为我方在上游侧的账户/参数/系统问题, 视为上游侧错误 (不计费)。
		return nil, busiErrf(fr.Code, fr.Msg, "", "")
	}

	var data faceCompareData
	_ = json.Unmarshal(fr.Data, &data)
	if !faceComparePaidCodes[data.Incorrect] {
		// 104/106/107/108/113 数据非法/系统异常/照片质量/图片过大/服务异常: 不收费,
		// 归一为上游侧错误交由 orchestrator 走 re-query/对账兜底。
		// 失败也带上游 orderNo(订单号)落审计供对账追查。
		return nil, busiErrf(data.Incorrect, fr.Msg, data.OrderNo, "")
	}
	return &model.UpstreamResult{
		Code:  "001",
		Msg:   "成功",
		UID:   data.OrderNo,
		Reqid: req.Reqid,
		LogID: data.OrderNo, // 只有 orderNo(订单号)一个上游标识，UID/LogID 同填供后台对账
		Range: compactJSON(fr.Data),
	}, nil
}

// Requery: 人脸身份证比对一所暂无幂等对账查询接口, 未联调前返回 Reachable=false,
// 记录保持 PENDING 由对账兜底 (与既有上游一致)。
func (c *FaceCompareClient) Requery(ctx context.Context, reqid string) (*model.RequeryResult, error) {
	_ = ctx
	_ = reqid
	return &model.RequeryResult{Reachable: false}, nil
}

// signFaceCompare 计算 sign = md5(appid&timestamp&app_security) (小写 hex)。
func signFaceCompare(appID, timestamp, appSecret string) string {
	sum := md5.Sum([]byte(appID + "&" + timestamp + "&" + appSecret))
	return hex.EncodeToString(sum[:])
}
