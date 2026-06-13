package main

import (
	"errors"

	"github.com/spf13/viper"
)

var (
	errDatabaseURLRequired     = errors.New("--database-url is required")
	errHarnessTokenRequired    = errors.New("--harness-token is required")
	errManagementTokenRequired = errors.New("--management-token is required")
	errInvalidBearerToken      = errors.New("invalid bearer token")
	errSessionIDRequired       = errors.New("session_id is required")
	errEventRequired           = errors.New("event is required")
	errNodeIDRequired          = errors.New("node_id is required")
)

type config struct {
	DatabaseURL     string
	HarnessAddr     string
	HarnessToken    string
	ManagementAddr  string
	ManagementToken string
	TLSCert         string
	TLSKey          string
}

func loadConfig(v *viper.Viper) (config, error) {
	cfg := config{
		DatabaseURL:     v.GetString("database-url"),
		HarnessAddr:     v.GetString("harness-addr"),
		HarnessToken:    v.GetString("harness-token"),
		ManagementAddr:  v.GetString("management-addr"),
		ManagementToken: v.GetString("management-token"),
		TLSCert:         v.GetString("tls-cert"),
		TLSKey:          v.GetString("tls-key"),
	}

	var errs []error

	if cfg.DatabaseURL == "" {
		errs = append(errs, errDatabaseURLRequired)
	}

	if cfg.HarnessToken == "" {
		errs = append(errs, errHarnessTokenRequired)
	}

	if cfg.ManagementToken == "" {
		errs = append(errs, errManagementTokenRequired)
	}

	// TLS is optional: omit cert/key for plain HTTP (dev only).
	return cfg, errors.Join(errs...)
}
