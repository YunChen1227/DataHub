//go:build ignore

package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/datahub/relay/internal/domain/model"
	"github.com/datahub/relay/internal/infrastructure/upstream"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))
	const (
		endpoint = "https://cisp.zenitera.com"
		orgCode  = "4098500006"
		ak       = "P8NIAURDVCQZBN7LGPTK"
		sk       = "PhICNDET1R1CuldEdhNih6HHKVfdfH6PtJe/DbYCCCcE"
		credit   = "92500233MA60R5KW8M"
	)
	httpClient := &http.Client{
		Timeout: 45 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec
	}
	client := upstream.NewEntCredit(upstream.EntCreditConfig{
		Endpoint: endpoint, OrgCode: orgCode, AccessKeyID: ak,
		SecretAccessKey: sk, Product: "P0130081",
	}, httpClient)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	_, err := client.Query(ctx, &model.UpstreamRequest{CreditCode: credit, Reqid: "raw-probe"})
	if err != nil {
		fmt.Println("FAIL:", err)
		return
	}
	fmt.Println("OK")
}
