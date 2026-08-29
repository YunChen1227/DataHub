// Package cache holds the「自然月结果缓存」domain logic: 缓存键推导、条目形态与
// TTL 计算。同一个人在同一自然月内的重复查询直接回放本月首查结果，跨月才回源上游。
//
// 设计要点（三条铁律，改动时勿破坏）：
//  1. 月份写进 key（qc:{group}:{YYYYMM}:{fingerprint}）。九月的查询根本不会去读
//     八月的 key，所以「跨月必须回源」这条业务规则不依赖 TTL 的精确性——TTL 退化
//     为纯粹的内存回收手段，可以自由加抖动而不影响语义。
//  2. 指纹用 HMAC 而非裸哈希。身份证号空间是可枚举的（出生日期+地区码+校验位），
//     裸 SHA-256 的 Redis 快照被拖走即可反查明文身份证；pepper 由配置注入，与
//     Redis 分开存放。
//  3. 只缓存「确定结论」（查得 001 / 查无 999）。上游错误/鉴权失败/参数非法一律
//     不入缓存，否则一次偶发的上游故障会被固化成整月的错误答案。
package cache

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/datahub/relay/internal/domain/model"
)

// cst 是判定「自然月」的基准时区。用固定偏移而非 time.LoadLocation("Asia/Shanghai")：
// 后者依赖宿主机/容器里的 tzdata，缺失时会静默回退 UTC——那会让月份边界错 8 小时。
// 中国无夏令时，固定 +08:00 恒等价。
var cst = time.FixedZone("CST", 8*60*60)

// keyPrefix 把结果缓存与同一个 Redis 逻辑库里的配额计数器 (quota:*) 分开。
const keyPrefix = "qc"

// fingerprintBytes 是指纹取的 HMAC 前缀字节数 (16B = 128bit = 32 hex)。128 位足以
// 让碰撞概率在十亿级 key 下仍可忽略，同时比全长省一半 key 内存。
const fingerprintBytes = 16

// ErrNoPepper 表示启用了缓存但没给 pepper。必须硬失败而非静默降级：没有 pepper 的
// HMAC 等于裸 SHA-256，身份证明文可被枚举反查（见包注释铁律 2）。
var ErrNoPepper = errors.New("缓存 pepper 未配置：没有 pepper 的指纹可被枚举反查身份证明文")

// Identity 是参与缓存键的查询要素（个人三要素路由：name 选填 + idCard + mobile）。
// 传姓名与不传姓名视为不同查询——上游入参不同，结果可能不同。
type Identity struct {
	Name   string
	IDCard string
	Mobile string
}

// IdentityOf 从上游请求提取并归一化查询要素。parse.Parse 已做过 TrimSpace/ToUpper，
// 这里重复一遍是防御性的（幂等），保证任何调用方进来都命中同一条缓存。
func IdentityOf(req *model.UpstreamRequest) Identity {
	return Identity{
		Name:   strings.TrimSpace(req.Name),
		IDCard: strings.ToUpper(strings.TrimSpace(req.IDCard)),
		Mobile: strings.TrimSpace(req.Mobile),
	}
}

// Entry 是缓存里存的一条结果（归一化后的 model.UpstreamResult + 溯源信息，不是上游
// 原始报文）。JSON 字段名刻意压到 1-2 个字符：每条 key 都要在 Redis 里驻留一整月，
// 字段名的开销会被乘以「月活去重人数」。
type Entry struct {
	Code   string `json:"c"` // 001 查得 / 999 查无
	Msg    string `json:"m"`
	UID    string `json:"u"` // 首次回源拿到的上游流水号，保留以便向上游对账
	LogID  string `json:"l"`
	Range  string `json:"r"` // 收入模型评分
	Verify string `json:"v"`
	// SrcRequestID 是首次回源那次请求的 requestId，可据此在审计表下钻到真正调用
	// 上游的那一行（缓存命中行没有上游标识来源）。
	SrcRequestID string `json:"sq"`
	CachedAt     int64  `json:"t"` // 首次回源的 Unix 秒
}

// Found 报告该条目是否为「查得数据」(计入成功查得数)。
func (e *Entry) Found() bool { return e != nil && e.Code == "001" }

// Cacheable 报告一个上游结论是否可入缓存：仅 001 查得与 999 查无属「确定结论」。
// 其余（上游业务错误码、网络失败、PENDING）一律不缓存，见包注释铁律 3。
func Cacheable(r *model.UpstreamResult) bool {
	return r != nil && (r.Code == "001" || r.Code == "999")
}

// EntryOf 把一次回源的确定结论装成缓存条目。调用方须先用 Cacheable 判定。
func EntryOf(r *model.UpstreamResult, requestID string, now time.Time) *Entry {
	return &Entry{
		Code:         r.Code,
		Msg:          r.Msg,
		UID:          r.UID,
		LogID:        r.LogID,
		Range:        r.Range,
		Verify:       r.Verify,
		SrcRequestID: requestID,
		CachedAt:     now.Unix(),
	}
}

// Result 把缓存条目还原成上游结果形态，供 mapping.Found/NotFound 复用同一套映射。
// reqid 传本次请求新生成的流水号：uid/logId 保留缓存原值（对账能追回上游那笔订单），
// 但下游看到的 reqid 每次唯一，不会被下游的去重逻辑误判成重复报文。
func (e *Entry) Result(reqid string) *model.UpstreamResult {
	return &model.UpstreamResult{
		Code:   e.Code,
		Msg:    e.Msg,
		UID:    e.UID,
		Reqid:  reqid,
		Range:  e.Range,
		Verify: e.Verify,
		LogID:  e.LogID,
	}
}

// Policy 是一条路由的缓存策略：共享组、指纹 pepper、TTL 抖动上界。
type Policy struct {
	group  string
	pepper []byte
	jitter time.Duration
}

// NewPolicy 构造缓存策略。group 为空时退化为路由名由调用方保证；pepper 为空直接
// 报错（ErrNoPepper），不允许静默降级成可枚举的裸哈希。
func NewPolicy(group, pepper string, jitter time.Duration) (Policy, error) {
	if strings.TrimSpace(pepper) == "" {
		return Policy{}, ErrNoPepper
	}
	if group == "" {
		return Policy{}, errors.New("缓存 group 不能为空")
	}
	if jitter < 0 {
		jitter = 0
	}
	return Policy{group: group, pepper: []byte(pepper), jitter: jitter}, nil
}

// Group 返回本策略的缓存共享组（同组路由共享同一份缓存）。
func (p Policy) Group() string { return p.group }

// Key 推导某时刻某身份的缓存键：qc:{group}:{YYYYMM}:{fingerprint}。
func (p Policy) Key(id Identity, now time.Time) string {
	return keyPrefix + ":" + p.group + ":" + monthBucket(now) + ":" + p.fingerprint(id)
}

// TTL 返回「到下个自然月 1 日 0 点」的剩余时长 + 随机抖动(0, jitter)。
//
// 抖动是为了避免几百万个 key 在月初同一瞬间集体到期——Redis 主动过期扫描每轮只处理
// 少量 key，海量同时到期会造成 CPU 尖刺与内存回收滞后。因为月份已在 key 里，抖动期
// 内残留的上月 key 永远不会被读到，纯属等待回收，对业务语义零影响。
func (p Policy) TTL(now time.Time) time.Duration {
	d := nextMonthStart(now).Sub(now)
	if d < time.Second {
		// 月末最后一刻写入：兜住 SETEX 不接受 <=0 的 TTL。
		d = time.Second
	}
	if p.jitter > 0 {
		d += time.Duration(rand.Int64N(int64(p.jitter)))
	}
	return d
}

// monthBucket 是自然月桶标识 (YYYYMM, +08:00)。
func monthBucket(t time.Time) string { return t.In(cst).Format("200601") }

// nextMonthStart 是下个自然月 1 日 00:00:00 (+08:00)。time.Date 会把 13 月归一化为
// 次年 1 月，故跨年无需特殊处理。
func nextMonthStart(t time.Time) time.Time {
	local := t.In(cst)
	return time.Date(local.Year(), local.Month()+1, 1, 0, 0, 0, 0, cst)
}

// fingerprint 是查询要素的 HMAC-SHA256 前 128 位（hex）。字段间插入 0x00 分隔，
// 避免 ("ab","c") 与 ("a","bc") 拼出同一条输入。
func (p Policy) fingerprint(id Identity) string {
	mac := hmac.New(sha256.New, p.pepper)
	mac.Write([]byte(id.Name))
	mac.Write([]byte{0})
	mac.Write([]byte(id.IDCard))
	mac.Write([]byte{0})
	mac.Write([]byte(id.Mobile))
	return hex.EncodeToString(mac.Sum(nil)[:fingerprintBytes])
}
