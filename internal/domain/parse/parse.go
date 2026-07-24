// Package parse validates and normalises the client request into a normalized
// upstream request shape (接口文档-经济能力.doc §3.1.3: mobile/idCard 必填, name
// 选填). The provider-specific verify/sign is filled later by the upstream client.
package parse

import (
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/datahub/relay/internal/common/errs"
	"github.com/datahub/relay/internal/domain/model"
)

var (
	mobileRe = regexp.MustCompile(`^1\d{10}$`)
	idCardRe = regexp.MustCompile(`^\d{17}[\dX]$`)
	// sfzhy 上游同时支持 15 位与 18 位身份证号 (接口文档注意事项第 8 条)。
	idCard15Re = regexp.MustCompile(`^\d{15}$`)
	// 统一社会信用代码 (GB 32100)：18 位，字符集不含 I/O/S/V/Z（不做校验位运算）。
	creditCodeRe = regexp.MustCompile(`^[0-9A-HJ-NPQRTUWXY]{2}\d{6}[0-9A-HJ-NPQRTUWXY]{10}$`)
)

// Parse runs参数校验; failures return busiCode 1007 数据请求异常 (我方拦截, 不调
// 上游/不计费). It generates an internal upstream reqid (≤20).
func Parse(cmd *model.QueryCommand) (*model.UpstreamRequest, error) {
	if cmd == nil {
		return nil, errs.New(errs.BusiDataRequestErr, "请求体为空")
	}
	name := strings.TrimSpace(cmd.Name) // 选填
	mobile := strings.TrimSpace(cmd.Mobile)
	idCard := strings.ToUpper(strings.TrimSpace(cmd.IDCard))

	if !mobileRe.MatchString(mobile) {
		return nil, errs.New(errs.BusiDataRequestErr, "mobile 格式非法")
	}
	if !idCardRe.MatchString(idCard) {
		return nil, errs.New(errs.BusiDataRequestErr, "idCard 格式非法")
	}

	return &model.UpstreamRequest{
		IDCard: idCard,
		Name:   name,
		Mobile: mobile,
		Reqid:  NewReqid(),
	}, nil
}

// ParseWithName 校验个人三要素且 name 必填。用于上游要求姓名必传的路由
// (zlf 租赁分：文档 §2.5 name 必；blk 黑名单V35：name 参与 MD5 摘要匹配)——
// 网关校验口径必须与上游要求一致，前置拦截而非透传给上游报错（对外手册承诺
// "参数非法不调用上游、不计费"）。
func ParseWithName(cmd *model.QueryCommand) (*model.UpstreamRequest, error) {
	up, err := Parse(cmd)
	if err != nil {
		return nil, err
	}
	if up.Name == "" {
		return nil, errs.New(errs.BusiDataRequestErr, "name 不能为空")
	}
	return up, nil
}

// ParseCreditCode 校验 swfp 入参 (creditCode 必填，统一社会信用代码；字段名对齐
// 上游证通 entcreditapi 的 args.creditCode——2026-07-08 上游 E1000 报错明确指出
// 必填字段名为 creditCode，不是官方 demo 文档示例里的 entInfo，不臆造中间层字段
// 名)。失败返回 busiCode 1007 数据请求异常 (我方拦截, 不调上游/不计费)。
func ParseCreditCode(cmd *model.QueryCommand) (*model.UpstreamRequest, error) {
	if cmd == nil {
		return nil, errs.New(errs.BusiDataRequestErr, "请求体为空")
	}
	creditCode := strings.ToUpper(strings.TrimSpace(cmd.CreditCode))
	if !creditCodeRe.MatchString(creditCode) {
		return nil, errs.New(errs.BusiDataRequestErr, "creditCode 格式非法")
	}
	return &model.UpstreamRequest{
		CreditCode: creditCode,
		Reqid:      NewReqid(),
	}, nil
}

// ParseFace 校验 rlbd1 (人脸身份证比对一所) 入参：name 必填、idCard 必填、
// image(base64) 与 url 二选一必填（对齐上游数脉 face_id_card/yisuo/compare 契约：
// name/idcard 必传，image/url 二选一）。网关前置拦截，失败返回 busiCode 1007
// 数据请求异常 (不调上游/不计费，与对外手册"参数非法直接拒绝"口径一致)。
func ParseFace(cmd *model.QueryCommand) (*model.UpstreamRequest, error) {
	if cmd == nil {
		return nil, errs.New(errs.BusiDataRequestErr, "请求体为空")
	}
	name := strings.TrimSpace(cmd.Name)
	idCard := strings.ToUpper(strings.TrimSpace(cmd.IDCard))
	image := strings.TrimSpace(cmd.Image)
	url := strings.TrimSpace(cmd.URL)

	if name == "" {
		return nil, errs.New(errs.BusiDataRequestErr, "name 不能为空")
	}
	if !idCardRe.MatchString(idCard) {
		return nil, errs.New(errs.BusiDataRequestErr, "idCard 格式非法")
	}
	if image == "" && url == "" {
		return nil, errs.New(errs.BusiDataRequestErr, "image 与 url 至少提供一个")
	}
	return &model.UpstreamRequest{
		Name:   name,
		IDCard: idCard,
		Image:  image,
		URL:    url,
		Reqid:  NewReqid(),
	}, nil
}

// ParseIDVerify 校验 sfzhy (身份证三要素核验) 入参：name 必填、idCard 必填
// (支持 15 位或 18 位)、profilePicture(base64 人像照片) 必填（对齐上游契约：
// Name/IdCard/ProfilePicture 均必传）。网关前置拦截，失败返回 busiCode 1007
// 数据请求异常 (不调上游/不计费)。
func ParseIDVerify(cmd *model.QueryCommand) (*model.UpstreamRequest, error) {
	if cmd == nil {
		return nil, errs.New(errs.BusiDataRequestErr, "请求体为空")
	}
	name := strings.TrimSpace(cmd.Name)
	idCard := strings.ToUpper(strings.TrimSpace(cmd.IDCard))
	picture := strings.TrimSpace(cmd.ProfilePicture)

	if name == "" {
		return nil, errs.New(errs.BusiDataRequestErr, "name 不能为空")
	}
	if !idCardRe.MatchString(idCard) && !idCard15Re.MatchString(idCard) {
		return nil, errs.New(errs.BusiDataRequestErr, "idCard 格式非法")
	}
	if picture == "" {
		return nil, errs.New(errs.BusiDataRequestErr, "profilePicture 不能为空")
	}
	return &model.UpstreamRequest{
		Name:           name,
		IDCard:         idCard,
		ProfilePicture: picture,
		Reqid:          NewReqid(),
	}, nil
}

// ParseConsumeTxn 校验 xfjy (消费交易特征) 入参。上游 data-bean 把 params 下的
// name/idcard/mobile/authlet 全部标为选填（接口文档「是否必填」列均为「否」），
// 故网关不强制某个具体字段必填（与上游必填口径一致，不臆造多余的必填约束）；
// 仅做两件事：① 对已提供的 idCard/mobile 校验格式（避免明显非法值触发无谓上游
// 调用）；② 要求至少提供一个查询要素 (name/idCard/mobile)，否则请求无实际查询
// 目标，前置拦截不调上游/不计费。失败返回 busiCode 1007 数据请求异常。
// 字段名对齐上游 params：name/idcard/mobile/authlet（authlet=终端授权书编号）。
func ParseConsumeTxn(cmd *model.QueryCommand) (*model.UpstreamRequest, error) {
	if cmd == nil {
		return nil, errs.New(errs.BusiDataRequestErr, "请求体为空")
	}
	name := strings.TrimSpace(cmd.Name)
	idCard := strings.ToUpper(strings.TrimSpace(cmd.IDCard))
	mobile := strings.TrimSpace(cmd.Mobile)
	authlet := strings.TrimSpace(cmd.Authlet)

	if name == "" && idCard == "" && mobile == "" {
		return nil, errs.New(errs.BusiDataRequestErr, "name/idCard/mobile 至少提供一个")
	}
	if idCard != "" && !idCardRe.MatchString(idCard) && !idCard15Re.MatchString(idCard) {
		return nil, errs.New(errs.BusiDataRequestErr, "idCard 格式非法")
	}
	if mobile != "" && !mobileRe.MatchString(mobile) {
		return nil, errs.New(errs.BusiDataRequestErr, "mobile 格式非法")
	}
	return &model.UpstreamRequest{
		Name:    name,
		IDCard:  idCard,
		Mobile:  mobile,
		Authlet: authlet,
		Reqid:   NewReqid(),
	}, nil
}

// reqidSeq guarantees in-process uniqueness even when the wall clock does not
// advance between two rapid calls (Windows time.Now() can have coarse ~ms
// granularity, so consecutive UnixNano() values may be identical and cause
// reqid collisions → idempotency replay).
var reqidSeq atomic.Uint64

// NewReqid generates an internal upstream reqid（base36 时间戳 + 进程内自增序号，
// ≤20 位，满足各上游 reqid ≤20 的约束并保证同进程内绝不重复）。
func NewReqid() string {
	ts := strconv.FormatInt(time.Now().UnixNano(), 36) // ≤13 位
	seq := strconv.FormatUint(reqidSeq.Add(1)%46656, 36) // 1–3 位 (36^3)
	r := ts + seq
	if len(r) > 20 {
		r = r[:20]
	}
	return r
}
