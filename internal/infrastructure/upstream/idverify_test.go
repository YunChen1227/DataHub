package upstream

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/datahub/relay/internal/domain/model"
)

// 计费判据必须是上游逐笔下发的 IsCharge（接口文档 §3「IsCharge 计费标志 本次请求
// 是否计费 true:计费,false:不计费」），而不是从 Code/Result 码表反推。
//
// 文档 §5.2 把 Result 0–5 全标了「计费」，所以正常情况 Code=0 恒计费；一旦上游改
// 口径先变的是 IsCharge，本表保证我方跟着变而不是继续按旧码表收钱。
func TestIDVerifyBillsByIsChargeFlag(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantCode string // "" 表示期望 error（上游侧异常，不计费）
	}{
		{
			name:     "Code=0 IsCharge=true → 001 计费",
			body:     `{"Code":0,"Message":"请求成功","IsCharge":true,"OutBizNo":"O-1","RequestId":"R-1","Data":{"Result":1,"ResultMessage":"识别为同一人","ImageScore":932.26}}`,
			wantCode: model.CodeFound,
		},
		{
			name:     "Code=0 但 IsCharge=false → 999 不计费（上游明说不收费，我方不得计费）",
			body:     `{"Code":0,"Message":"请求成功","IsCharge":false,"OutBizNo":"O-2","RequestId":"R-2","Data":{"Result":1,"ResultMessage":"识别为同一人","ImageScore":900}}`,
			wantCode: model.CodeNotFound,
		},
		{
			name: "Code=461 照片不合规 IsCharge=false → 上游侧错误",
			body: `{"Code":461,"Message":"请求照片大小不符合要求","IsCharge":false,"ErrorAddress":"00000","RequestId":"R-3"}`,
		},
		{
			name: "Code=408 余额不足 IsCharge=false → 上游侧错误",
			body: `{"Code":408,"Message":"账号余额不足","IsCharge":false,"RequestId":"R-4"}`,
		},
		{
			name: "Code≠0 却 IsCharge=true → 仍按上游侧错误返回（下游拿不到数据就不该被收费），落 warn 供人工对账",
			body: `{"Code":502,"Message":"系统内部错误","IsCharge":true,"RequestId":"R-5"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			c := NewIDVerify(IDVerifyConfig{BaseURL: srv.URL, AppID: "ap1", AppSecret: "sec1"}, srv.Client())
			res, err := c.Query(context.Background(), &model.UpstreamRequest{
				Name: "张三", IDCard: "420101198012010011", ProfilePicture: "aGVsbG8=", Reqid: "r1",
			})

			if tc.wantCode == "" {
				if err == nil {
					t.Fatalf("期望上游侧错误，实际成功: code=%s", res.Code)
				}
				var be *model.UpstreamError
				if !errors.As(err, &be) {
					t.Fatalf("期望 *model.UpstreamError（带上游 code/uid 落审计），实际 %T: %v", err, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Query: %v", err)
			}
			if res.Code != tc.wantCode {
				t.Fatalf("code = %s, 期望 %s", res.Code, tc.wantCode)
			}
			// mapping.NotFound/Found 把 Msg 原样写进下游 body.msg，所以归一化时写的
			// 每个 Msg 都是对客文案。「上游/计费标志」这类内部归因只能进日志。
			for _, leak := range []string{"上游", "计费", "IsCharge"} {
				if strings.Contains(res.Msg, leak) {
					t.Fatalf("body.msg %q 含内部措辞 %q——该字段会原样下发给客户", res.Msg, leak)
				}
			}
		})
	}
}
