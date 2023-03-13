package main

import (
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

func Scheme(r *http.Request) (scheme string) {
	scheme = r.Header.Get("X-Forwarded-Proto")
	if scheme == "" {
		scheme = "https"
	}
	return
}

func IP(r *http.Request) (ip net.IP) {
	ip = net.ParseIP(r.Header.Get("X-Real-IP"))
	return
}

func ASN(ip net.IP) (asn string) {
	client := http.Client{
		Timeout: 500 * time.Millisecond,
	}
	req := config.IPASNURL + "/" + ip.String()
	resp, err := client.Get(req)
	if err != nil {
		logger.Errorf("IPASN HTTP Get failed: %v\n", err)
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Errorf("IPASN read body failed: %v\n", err)
		return
	}
	asn = string(body)
	return
}

func Host(r *http.Request) (labels []string) {
	dots := strings.Split(r.Header.Get("X-Forwarded-Host"), ".")
	if len(dots) != config.DomainLength {
		return
	}
	labels = strings.Split(dots[0], "-")
	return
}
