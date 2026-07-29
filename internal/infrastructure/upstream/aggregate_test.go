package upstream

import (
	"context"
	"errors"
	"testing"

	"github.com/datahub/relay/internal/domain/model"
)

// fakePort 是一个可编排返回值的上游子源桩，用于聚合器行为测试。
type fakePort struct {
	res *model.UpstreamResult
	err error
}

func (f fakePort) Query(context.Context, *model.UpstreamRequest) (*model.UpstreamResult, error) {
	return f.res, f.err
}
func (f fakePort) Requery(context.Context, string) (*model.RequeryResult, error) {
	return &model.RequeryResult{Reachable: false}, nil
}

// TestAggregatorAllFailedCarriesUpstreamIDs 锁定「上游 requestId 无论成功失败都必须
// 落审计」铁律的聚合器分支：当所有子源都以业务失败（*model.UpstreamError，携带上游
// 订单号/请求号）返回时，聚合器必须回传一个 *model.UpstreamError，把子源的
// uid/logId/code 带出，供 orchestrator 写进审计——绝不能退化成裸 error 让三列变空。
func TestAggregatorAllFailedCarriesUpstreamIDs(t *testing.T) {
	sources := []LabeledUpstream{
		{Label: "invoice1", Port: fakePort{err: &model.UpstreamError{Code: "E1099", Msg: "无权限", UID: "ORD-A", LogID: "ORD-A"}}},
		{Label: "tax1", Port: fakePort{err: &model.UpstreamError{Code: "E1010", Msg: "余额不足", UID: "ORD-B", LogID: "ORD-B"}}},
	}
	agg, err := NewAggregator(sources)
	if err != nil {
		t.Fatalf("NewAggregator: %v", err)
	}

	res, callErr := agg.Query(context.Background(), &model.UpstreamRequest{Reqid: "r1"})
	if res != nil {
		t.Fatalf("expected nil result on all-failed, got %+v", res)
	}
	var ue *model.UpstreamError
	if !errors.As(callErr, &ue) {
		t.Fatalf("all-failed 必须返回 *model.UpstreamError（否则审计三列为空），got %T: %v", callErr, callErr)
	}
	if ue.UID == "" || ue.LogID == "" {
		t.Fatalf("聚合失败必须带上游 uid/logId，got uid=%q logId=%q", ue.UID, ue.LogID)
	}
	if ue.Code == "" {
		t.Fatalf("聚合失败必须带上游 code，got 空")
	}
	// uid/logId 取任一子源的上游标识即可（本例第一个非空）。
	if ue.UID != "ORD-A" || ue.LogID != "ORD-A" {
		t.Fatalf("期望取首个非空子源标识 ORD-A，got uid=%q logId=%q", ue.UID, ue.LogID)
	}
}

// TestAggregatorPartialFailureCarriesUpstreamIDs 部分成功(002)时也应带上游标识。
func TestAggregatorPartialFailureCarriesUpstreamIDs(t *testing.T) {
	sources := []LabeledUpstream{
		{Label: "invoice1", Port: fakePort{err: &model.UpstreamError{Code: "E1010", Msg: "余额不足", UID: "ORD-FAIL", LogID: "ORD-FAIL"}}},
		{Label: "tax1", Port: fakePort{res: &model.UpstreamResult{Code: "999", Msg: "查无", UID: "ORD-OK", LogID: "ORD-OK"}}},
	}
	agg, _ := NewAggregator(sources)
	res, callErr := agg.Query(context.Background(), &model.UpstreamRequest{Reqid: "r2"})
	if callErr != nil {
		t.Fatalf("部分成功不应返回 error: %v", callErr)
	}
	if res == nil || res.Code != "002" {
		t.Fatalf("期望 002 部分成功, got %+v", res)
	}
	if res.UID == "" || res.LogID == "" {
		t.Fatalf("部分成功也必须带上游 uid/logId, got uid=%q logId=%q", res.UID, res.LogID)
	}
}
