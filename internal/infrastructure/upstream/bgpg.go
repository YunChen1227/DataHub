package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/datahub/relay/internal/domain/model"
)

// 背景评估 BJPG-01 契约常量，逐字对齐 docs/BJPG-01背景评估 (2)(1).pdf。
const (
	// BgPGProdID 是产品编号，随请求头 prodId 传给上游 (文档 §1 产品列表 / §4.1 请求头)。
	BgPGProdID = "BJPG-01"

	// bgpgIV 是 AES/CBC 初始向量：官方工具类 AesUtil 写死
	// new IvParameterSpec("0000000000000000".getBytes())，即 16 个 ASCII 字符 '0'
	// (0x30)，**不是** 16 个零字节。
	bgpgIV = "0000000000000000"
)

// 返回体结果码 (文档 §4.3 码表)。只有 200 计费，其余全部不计费；其中 2-404 与
// 3-404 都表示「没有查询到」，归一为查无，其余归一为上游侧错误：
//
//	2-500 服务器内部异常      2-501 解密失败，请检查 data 加密方式
//	2-502 参数不全或者参数不正确  2-503 请求 token 失败
//	2-504 无该接口访问权限      2-505 今日调用次数超过限额
//	2-506 请求头中缺少 accountId 2-507 请求头中 accountId 不正确
//	2-508 请求 ip 不在白名单内   2-509 账户已经停止使用
//	3-510 查询失败            3-506 权限问题            3-507 获取数据失败
const (
	bgpgCodeOK = "200" // 请求成功 (计费)
	// 查无两个码：2-404「没有查询到数据」与 3-404「没有查询到」。
	bgpgCodeNotFound      = "2-404"
	bgpgCodeNotFoundOuter = "3-404"
)

// BgPGConfig holds the 背景评估 BJPG-01 endpoint + 我方在该上游侧的凭证 (开户时下发
// accountId / encryptKey，文档 §2 开户步骤 / §3 工具类)。
type BgPGConfig struct {
	BaseURL string // 生产 http://122.224.147.153:13188/api/getData；测试 :13199
	// AccountID 即请求头 accountId (账户 id)。
	AccountID string
	// ProdID 即请求头 prodId (产品 id)，缺省 BJPG-01。
	ProdID string
	// EncryptKey 是上游下发的 encryptKey：**hex 文本**，解码后才是 AES 密钥
	// (官方 demo: Hex.decodeHex(key.toCharArray()))。文档示例 32 个 hex 字符
	// = 16 字节 = AES-128。
	EncryptKey string
}

// BgPGClient implements port.UpstreamPort for the 背景评估 BJPG-01 provider。
//
// 协议 (文档 §4)：POST JSON {"data": Base64(AES/CBC/PKCS5(明文业务JSON))}，
// 请求头带 accountId + prodId；明文业务 JSON 只有 idCard + name 两个字段，均必填
// (文档 §4.2)——该上游**不要手机号**。响应 {"data","code","uuid","retMsg"}，
// data 为同一套 AES 密文，解密得 {xm,sfz,jfdw,grsf,jfjs,cbjfzt,jfsj}。
//
// 归一：code 200 → "001" 查得 (Range = 解密后业务对象的 compact JSON，全字段原样
// 透出)；2-404 / 3-404 → "999" 查无；其余 → 上游侧错误 (不计费，走复查/对账兜底)。
//
// 上游标识：响应只有 uuid 一个可对账的标识，故 UID 与 LogID 同填 uuid。
type BgPGClient struct {
	cfg  BgPGConfig
	http *http.Client
	key  []byte // encryptKey 的 hex 解码值 (16/24/32 字节)
	// keyErr 记录「凭证未配置」——memory/未配置模式下服务仍要能启动，故不在 New 阶段
	// 阻塞，改为调用时报错。encryptKey 配了但形态非法则由 NewBgPG 直接返回 error。
	keyErr error
}

// NewBgPG builds a 背景评估 client (prodId 缺省 BJPG-01)。encryptKey 已配置但形态
// 非法 (非 hex / 长度不是 16·24·32 字节) 时返回 error 令服务启动即失败——密钥错时
// 一个请求都发不出去，必须在启动期暴露而不是留到线上加密失败。
func NewBgPG(cfg BgPGConfig, httpClient *http.Client) (*BgPGClient, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if cfg.ProdID == "" {
		cfg.ProdID = BgPGProdID
	}
	c := &BgPGClient{cfg: cfg, http: httpClient}
	if strings.TrimSpace(cfg.EncryptKey) == "" {
		c.keyErr = errors.New("bgpg encryptKey 未配置")
		return c, nil
	}
	key, err := aesKeyFromHex(strings.TrimSpace(cfg.EncryptKey))
	if err != nil {
		return nil, fmt.Errorf("bgpg encryptKey 非法: %w", err)
	}
	c.key = key
	return c, nil
}

// bgpgParams 是加密前的明文业务 JSON (文档 §4.2 请求体)：idCard 与 name 均必填，
// 字段名逐字照抄上游参数表。
type bgpgParams struct {
	IDCard string `json:"idCard"`
	Name   string `json:"name"`
}

// bgpgRequest 是外层请求体：只有一个 data 字段承载密文 (文档 §4.2 第 3 步「将
// dataValue 值放入请求 body 中」，字段名由结果码 2-501「解密失败，请检查 data
// 加密方式」印证)。
type bgpgRequest struct {
	Data string `json:"data"`
}

// bgpgResponse 是返回体外层 (文档 §4.3)。data 仅在 code=200 时有值且为 AES 密文；
// uuid 是唯一可用于向上游对账的标识。
type bgpgResponse struct {
	Data   string `json:"data"`
	Code   string `json:"code"`
	UUID   string `json:"uuid"`
	RetMsg string `json:"retMsg"`
}

// Query 加密入参后发起签名 POST 并归一化响应 (见 BgPGClient 文档注释)。
func (c *BgPGClient) Query(ctx context.Context, req *model.UpstreamRequest) (*model.UpstreamResult, error) {
	if c.keyErr != nil {
		return nil, c.keyErr
	}

	plain, err := json.Marshal(bgpgParams{IDCard: req.IDCard, Name: req.Name})
	if err != nil {
		return nil, fmt.Errorf("marshal bgpg params: %w", err)
	}
	data, err := aesCBCEncryptBase64(plain, c.key, []byte(bgpgIV))
	if err != nil {
		return nil, fmt.Errorf("encrypt bgpg data: %w", err)
	}
	body, err := json.Marshal(bgpgRequest{Data: data})
	if err != nil {
		return nil, fmt.Errorf("marshal bgpg request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build bgpg request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json;charset=utf-8")
	httpReq.Header.Set("accountId", c.cfg.AccountID)
	httpReq.Header.Set("prodId", c.cfg.ProdID)

	slog.Debug("bgpg request", "url", c.cfg.BaseURL, "accountId", c.cfg.AccountID,
		"prodId", c.cfg.ProdID, "reqid", req.Reqid)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("bgpg call: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read bgpg body: %w", err)
	}
	slog.Debug("bgpg response", "status", resp.StatusCode, "raw", string(raw))

	var br bgpgResponse
	if err := json.Unmarshal(raw, &br); err != nil {
		return nil, fmt.Errorf("decode bgpg body (http %d): %w", resp.StatusCode, err)
	}

	switch br.Code {
	case bgpgCodeOK:
		rng, err := c.decodeData(br.Data)
		if err != nil {
			// 已应答成功码但结果解密失败——按上游侧错误处理 (不计费)，带上 uuid 供对账。
			return nil, busiErr(br.Code, "data 解密失败: "+err.Error(), br.UUID, br.UUID)
		}
		return &model.UpstreamResult{
			Code:  "001",
			Msg:   "成功",
			UID:   br.UUID,
			Reqid: req.Reqid,
			LogID: br.UUID, // 上游仅 uuid 一个标识，UID/LogID 同填供后台对账
			Range: rng,
		}, nil
	case bgpgCodeNotFound, bgpgCodeNotFoundOuter:
		return &model.UpstreamResult{
			Code:  "999",
			Msg:   "没有查询到数据",
			UID:   br.UUID,
			Reqid: req.Reqid,
			LogID: br.UUID,
		}, nil
	default:
		// 2-5xx / 3-5xx 均为我方在上游侧的账户/参数/系统问题：不计费，交由 orchestrator
		// 走复查/对账兜底。失败也带 uuid 落审计供对账追查 (禁止裸 fmt.Errorf)。
		return nil, busiErr(br.Code, br.RetMsg, br.UUID, br.UUID)
	}
}

// decodeData 解开返回体 data 密文并压紧为下游 result.range 透出的 JSON 字符串
// (全字段原样透出 {xm,sfz,jfdw,grsf,jfjs,cbjfzt,jfsj}，不裁剪)。code=200 但 data
// 为空串时返回空 range，不视为错误。
func (c *BgPGClient) decodeData(data string) (string, error) {
	if strings.TrimSpace(data) == "" {
		return "", nil
	}
	plain, err := aesCBCDecryptBase64(strings.TrimSpace(data), c.key, []byte(bgpgIV))
	if err != nil {
		return "", err
	}
	return compactJSON(plain), nil
}

// Requery: 背景评估 getData 无对账查询接口 (文档只定义了一个业务接口)。返回
// Reachable=false，记录保持 PENDING 由对账兜底 (与既有上游一致)。
func (c *BgPGClient) Requery(ctx context.Context, reqid string) (*model.RequeryResult, error) {
	_ = ctx
	_ = reqid
	return &model.RequeryResult{Reachable: false}, nil
}
