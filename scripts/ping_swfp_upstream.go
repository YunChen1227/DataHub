//go:build ignore
package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"time"

	"github.com/datahub/relay/internal/domain/model"
	"github.com/datahub/relay/internal/infrastructure/upstream"
)

func main() {
	endpoints := []string{
		"https://cisp.zenitera.com",
		"https://cisp.ect888.com",
		"https://112.65.144.19:9179",
	}
	httpClient := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec
	}
	cc := "911101055695184024"
	for _, ep := range endpoints {
		fmt.Printf("\n--- endpoint %s ---\n", ep)
		client := upstream.NewEntCredit(upstream.EntCreditConfig{
			Endpoint:        ep,
			OrgCode:         "0100600007",
			AccessKeyID:     "demo-swfp-ak",
			SecretAccessKey: "ZGVtby1zd2ZwLXNrLTMyLWJ5dGVzLWxvbmctc2VjcmV0",
			Product:         "P0130081",
		}, httpClient)
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_, err := client.Query(ctx, &model.UpstreamRequest{CreditCode: cc, Reqid: "ping"})
		cancel()
		d := time.Since(start)
		if err != nil {
			fmt.Printf("elapsed=%s err=%v\n", d, err)
		} else {
			fmt.Printf("elapsed=%s OK\n", d)
		}
	}
}
