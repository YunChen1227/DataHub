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
	// xfjy 授权书编号 authlet：由数字+字母组成（data-bean fk3002 字段说明）。
	authletRe = regexp.MustCompile(`^[0-9A-Za-z]+$`)
	// tsfx 命中级别策略 poly：C1 高危 / C2 敏感 / C3 一般（kfongtech 参数表枚举）。
	polyRe = regexp.MustCompile(`^C[123]$`)
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

// ParseConsumeTxn 校验 xfjy (消费交易特征) 入参。字段口径严格对齐上游 data-bean
// fk3002 的 params 契约：name/idcard/mobile/authlet 四个私有字段，其中
// authlet（终端授权书编号：被查个人主体授予机构查询本身信息的授权代码，由数字+
// 字母组成）为**认证必填**——缺失将无法通过上游合规校验，故网关前置强制必填、
// 缺失即拦截不调上游/不计费（不同上游字段与必填口径各不相同，此处以本上游为准）。
// 校验规则：① authlet 必填且须为数字+字母；② 至少提供一个身份要素
// (name/idCard/mobile)，否则请求无实际查询目标；③ 对已提供的 idCard/mobile
// 校验格式，避免明显非法值触发无谓上游调用。失败返回 busiCode 1007 数据请求异常。
func ParseConsumeTxn(cmd *model.QueryCommand) (*model.UpstreamRequest, error) {
	if cmd == nil {
		return nil, errs.New(errs.BusiDataRequestErr, "请求体为空")
	}
	name := strings.TrimSpace(cmd.Name)
	idCard := strings.ToUpper(strings.TrimSpace(cmd.IDCard))
	mobile := strings.TrimSpace(cmd.Mobile)
	authlet := strings.TrimSpace(cmd.Authlet)

	if authlet == "" {
		return nil, errs.New(errs.BusiDataRequestErr, "authlet(授权书编号) 必填")
	}
	if !authletRe.MatchString(authlet) {
		return nil, errs.New(errs.BusiDataRequestErr, "authlet(授权书编号) 格式非法, 须由数字与字母组成")
	}
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

// ParseComplaint 校验 tsfx (投诉分析识别名单) 入参。字段口径严格对齐上游 kfongtech
// api.complaint.query 的业务参数表：mobile（手机号，必填）+ poly（命中级别策略，
// 必填，枚举 C1/C2/C3）。method/version 为固定常量由上游客户端填充，非下游入参。
// 校验规则：① mobile 必填且须为 11 位手机号；② poly 必填且须为 C1/C2/C3。
// 失败返回 busiCode 1007 数据请求异常（我方前置拦截，不调上游/不计费）。
func ParseComplaint(cmd *model.QueryCommand) (*model.UpstreamRequest, error) {
	if cmd == nil {
		return nil, errs.New(errs.BusiDataRequestErr, "请求体为空")
	}
	mobile := strings.TrimSpace(cmd.Mobile)
	poly := strings.ToUpper(strings.TrimSpace(cmd.Poly))

	if !mobileRe.MatchString(mobile) {
		return nil, errs.New(errs.BusiDataRequestErr, "mobile 格式非法")
	}
	if !polyRe.MatchString(poly) {
		return nil, errs.New(errs.BusiDataRequestErr, "poly(命中级别) 必填, 须为 C1/C2/C3 之一")
	}
	return &model.UpstreamRequest{
		Mobile: mobile,
		Poly:   poly,
		Reqid:  NewReqid(),
	}, nil
}

// ParseBgPG 校验 grsb (背景评估 BJPG-01) 入参。字段口径严格对齐上游参数表
// (docs/BJPG-01背景评估 §4.2 请求体)：加密前的明文业务 JSON 只有 idCard 与 name
// 两个字段，**都标必填，且没有 mobile**——故网关前置要求 name + idCard 齐全，
// 并且不校验、不透传手机号（不同上游字段集合各不相同，此处以本上游文档为唯一依据，
// 不沿用其它路由的三要素默认集合）。失败返回 busiCode 1007 数据请求异常
// (我方拦截，不调上游/不计费)。
func ParseBgPG(cmd *model.QueryCommand) (*model.UpstreamRequest, error) {
	if cmd == nil {
		return nil, errs.New(errs.BusiDataRequestErr, "请求体为空")
	}
	name := strings.TrimSpace(cmd.Name)
	idCard := strings.ToUpper(strings.TrimSpace(cmd.IDCard))

	if name == "" {
		return nil, errs.New(errs.BusiDataRequestErr, "name 不能为空")
	}
	if !idCardRe.MatchString(idCard) {
		return nil, errs.New(errs.BusiDataRequestErr, "idCard 格式非法")
	}
	return &model.UpstreamRequest{
		Name:   name,
		IDCard: idCard,
		Reqid:  NewReqid(),
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
