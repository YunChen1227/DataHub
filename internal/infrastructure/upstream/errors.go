package upstream

import (
	"strconv"

	"github.com/datahub/relay/internal/domain/model"
)

// busiErr 构造一个带上游标识的业务错误 (*model.UpstreamError)。当上游"已应答但
// 以非成功业务码明确拒绝/失败"时，各 client 用它替代裸 fmt.Errorf，使 orchestrator
// 能把上游返回的 code/msg/uid(订单号)/logID(请求号) 落进审计——失败也可向上游对账
// 追查。纯网络/传输失败(上游不可达)不要用它(没有上游标识)，仍用 fmt.Errorf。
func busiErr(code, msg, uid, logID string) error {
	return &model.UpstreamError{Code: code, Msg: msg, UID: uid, LogID: logID}
}

// busiErrf 同 busiErr，但上游 code 为整型数值码(如 461/1002/200)。
func busiErrf(code int, msg, uid, logID string) error {
	return &model.UpstreamError{Code: strconv.Itoa(code), Msg: msg, UID: uid, LogID: logID}
}
